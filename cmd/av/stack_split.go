package main

import (
	"emperror.dev/errors"
	"github.com/aviator-co/av/internal/actions"
	"github.com/aviator-co/av/internal/git"
	"github.com/aviator-co/av/internal/meta"
	"github.com/aviator-co/av/internal/sequencer"
	"github.com/aviator-co/av/internal/sequencer/planner"
	"github.com/aviator-co/av/internal/sequencer/sequencerui"
	"github.com/aviator-co/av/internal/utils/cleanup"
	"github.com/aviator-co/av/internal/utils/uiutils"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var stackSplitFlags struct {
	BranchName string
}
var stackSplitCmd = &cobra.Command{
	Use:     "split <branch-name>",
	Short:   "Split the current stack onto a new stack branch.",
	Aliases: []string{"sp"},
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := getRepo()
		if err != nil {
			return err
		}

		db, err := getDB(repo)
		if err != nil {
			return err
		}

		if len(args) > 0 && args[0] == "" {
			return errors.New("Must provide a branch name.")
		}

		stackSplitFlags.BranchName = args[0]
		stackSplitFlags.BranchName = stripRemoteRefPrefixes(repo, stackSplitFlags.BranchName)
		return uiutils.RunBubbleTea(&stackSplitViewModel{repo: repo, db: db})
	},
}

type stackSplitViewModel struct {
	repo *git.Repo
	db   meta.DB

	stackSplitModel *sequencerui.RestackModel

	quitWithConflict bool
	err              error
}

func (vm *stackSplitViewModel) Init() tea.Cmd {
	state, err := vm.createState()
	if err != nil {
		return func() tea.Msg { return err }
	}
	vm.stackSplitModel = sequencerui.NewRestackModel(vm.repo, vm.db)
	vm.stackSplitModel.State = state
	return vm.stackSplitModel.Init()
}

func (vm *stackSplitViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case *sequencerui.RestackProgress, spinner.TickMsg:
		var cmd tea.Cmd
		vm.stackSplitModel, cmd = vm.stackSplitModel.Update(msg)
		return vm, cmd
	case *sequencerui.RestackConflict:
		if err := vm.writeState(vm.stackSplitModel.State); err != nil {
			return vm, func() tea.Msg { return err }
		}
		vm.quitWithConflict = true
		return vm, tea.Quit
	case *sequencerui.RestackAbort, *sequencerui.RestackDone:
		if err := vm.writeState(nil); err != nil {
			return vm, func() tea.Msg { return err }
		}
		return vm, tea.Quit
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return vm, tea.Quit
		}
	case error:
		vm.err = msg
		return vm, tea.Quit
	}
	return vm, nil
}

func (vm *stackSplitViewModel) View() string {
	var ss []string
	ss = append(ss, "Splitting onto "+stackSplitFlags.BranchName+"...")
	if vm.stackSplitModel != nil {
		ss = append(ss, vm.stackSplitModel.View())
	}

	var ret string
	if len(ss) != 0 {
		ret = lipgloss.NewStyle().MarginTop(1).MarginBottom(1).MarginLeft(2).Render(
			lipgloss.JoinVertical(0, ss...),
		)
	}
	if vm.err != nil {
		if len(ret) != 0 {
			ret += "\n"
		}
		ret += renderError(vm.err)
	}
	return ret
}

func (vm *stackSplitViewModel) writeState(state *sequencerui.RestackState) error {
	if state == nil {
		return vm.repo.WriteStateFile(git.StateFileKindRestack, nil)
	}
	return vm.repo.WriteStateFile(git.StateFileKindRestack, state)
}

func (vm *stackSplitViewModel) createState() (*sequencerui.RestackState, error) {
	currentBranch, err := vm.repo.CurrentBranchName()
	if err != nil {
		return nil, err
	}
	isCurrentBranchTrunk, err := vm.repo.IsTrunkBranch(currentBranch)
	if err != nil {
		return nil, err
	} else if isCurrentBranchTrunk {
		return nil, errors.New("current branch is a trunk branch")
	}
	branch, e := vm.db.ReadTx().Branch(currentBranch)
	if !e {
		return nil, errors.New("current branch is not adopted to av")
	}
	status, err := vm.repo.Status()
	if err != nil {
		return nil, errors.Wrapf(err, "Cannot get the status of the repository")
	}
	// abort action if the current branch has any stage change
	if !status.IsClean() {
		return nil, errors.New("Current repository is not clean, all the changes has to be commited")
	}
	if _, err := vm.repo.DoesBranchExist(stackSplitFlags.BranchName); err != nil {
		return nil, errors.Errorf("failed the branch %s already exists", stackSplitFlags.BranchName)
	}

	newBranchName := stackSplitFlags.BranchName
	parentBranch := meta.LastChildOf(vm.db.ReadTx(), branch.Name)
	if parentBranch == nil {
		return nil, errors.Errorf("parent branch %s does not exist", branch.Name)
	}

	tx := vm.db.WriteTx()
	cu := cleanup.New(func() {
		logrus.WithError(err).Debug("aborting db transaction")
		tx.Abort()
	})
	defer cu.Cleanup()
	defer cu.Cancel()

	// Create a new branch off of the parent
	if _, err := vm.repo.CheckoutBranch(&git.CheckoutBranch{
		Name:       newBranchName,
		NewBranch:  true,
		NewHeadRef: parentBranch.Name,
	}); err != nil {
		return nil, errors.WrapIff(err, "checkout error")
	}

	mergeBase, err := vm.repo.MergeBase(parentBranch.Name, newBranchName)
	if err != nil {
		return nil, err
	}
	tx.SetBranch(meta.Branch{
		Name: newBranchName,
		Parent: meta.BranchState{
			Name:  parentBranch.Name,
			Trunk: isCurrentBranchTrunk,
			Head:  mergeBase,
		},
	})

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// expect to be check-in on the new branch
	// new stacked branch is added as the new bottom of the tree;
	// all|N commits were rebased for the new stack
	currentBranch, err = vm.repo.CurrentBranchName()
	if currentBranch != newBranchName || err != nil {
		return nil, errors.Wrapf(err, "failed validating current branch, expected to be new %s current is %s ", newBranchName, currentBranch)
	}

	var state sequencerui.RestackState
	state.InitialBranch = currentBranch
	state.RelatedBranches = []string{currentBranch, stackReparentFlags.Parent}
	ops, err := planner.PlanForReparent(
		vm.db.ReadTx(),
		vm.repo,
		plumbing.NewBranchReferenceName(currentBranch),
		plumbing.NewBranchReferenceName(stackReparentFlags.Parent),
	)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, nothingToRestackError
	}
	state.Seq = sequencer.NewSequencer(vm.repo.GetRemoteName(), vm.db, ops)
	return &state, nil
}

func (vm *stackSplitViewModel) ExitError() error {
	if errors.Is(vm.err, nothingToRestackError) {
		return nil
	}
	if vm.err != nil {
		return actions.ErrExitSilently{ExitCode: 1}
	}
	if vm.quitWithConflict {
		return actions.ErrExitSilently{ExitCode: 1}
	}
	return nil
}

func init() {
	stackSplitCmd.Flags().
		StringVar(&stackSplitFlags.BranchName, "branch-name", "", "the branch name")
	stackSplitCmd.MarkFlagsMutuallyExclusive("branch-name")
}
