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
	"strings"
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
	if isCurrentBranchTrunk, err := vm.repo.IsTrunkBranch(currentBranch); err != nil {
		return nil, err
	} else if isCurrentBranchTrunk {
		return nil, errors.New("current branch is a trunk branch")
	}
	if _, exist := vm.db.ReadTx().Branch(currentBranch); !exist {
		return nil, errors.New("current branch is not adopted to av")
	}

	newBranchName := stackSplitFlags.BranchName
	parentBranch := meta.LastChildOf(vm.db.ReadTx(), currentBranch)
	if parentBranch == nil {
		return nil, errors.New("last stack branch does not exist")
	}

	tx := vm.db.WriteTx()
	cu := cleanup.New(func() {
		logrus.WithError(err).Debug("aborting db transaction")
		tx.Abort()
	})
	defer cu.Cleanup()

	remoteName := vm.repo.GetRemoteName()
	parentBranchName := strings.TrimPrefix(parentBranch.Name, remoteName+"/")
	checkoutStartingPoint := parentBranchName
	isParentTrunk := parentBranch.IsStackRoot()
	var parentHead string
	if isParentTrunk {
		defaultBranch, err := vm.repo.DefaultBranch()
		if err != nil {
			return nil, errors.WrapIf(err, "failed to determine repository default branch")
		}
		// If the parent is trunk, start from the remote tracking branch.
		checkoutStartingPoint = remoteName + "/" + defaultBranch
		// If the parent is the trunk, we don't log the parent branch's head
		parentHead = ""
	} else {
		var err error
		parentHead, err = vm.repo.RevParse(&git.RevParse{Rev: parentBranch.Name})
		if err != nil {
			return nil, errors.WrapIff(
				err,
				"failed to determine head commit of branch %q",
				parentHead,
			)
		}
	}

	startPointCommitHash, err := vm.repo.RevParse(&git.RevParse{Rev: checkoutStartingPoint})
	if err != nil {
		return nil, errors.WrapIf(err, "failed to determine commit hash of starting point")
	}

	if _, err = vm.repo.CheckoutBranch(&git.CheckoutBranch{
		Name:       newBranchName,
		NewBranch:  true,
		NewHeadRef: startPointCommitHash,
	}); err != nil {
		return nil, errors.WrapIff(err, "checkout error")
	}

	tx.SetBranch(meta.Branch{
		Name: newBranchName,
		Parent: meta.BranchState{
			Name:  parentBranch.Name,
			Trunk: isParentTrunk,
			Head:  parentHead,
		},
	})

	var state sequencerui.RestackState
	state.InitialBranch = parentBranch.Name
	state.RelatedBranches = []string{parentBranch.Name, newBranchName}
	ops, err := planner.PlanForReparent(
		vm.db.ReadTx(),
		vm.repo,
		plumbing.NewBranchReferenceName(parentBranch.Name),
		plumbing.NewBranchReferenceName(newBranchName),
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
