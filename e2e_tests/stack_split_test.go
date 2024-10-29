package e2e_tests

import (
	"github.com/aviator-co/av/internal/meta"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/aviator-co/av/internal/git/gittest"
)

func TestStackSplit(t *testing.T) {
	server := RunMockGitHubServer(t)
	defer server.Close()
	repo := gittest.NewTempRepoWithGitHubServer(t, server.URL)
	Chdir(t, repo.RepoDir)

	t.Run("FlagAll", func(t *testing.T) {

		RequireAv(t, "stack", "branch", "stack-1")
		repo.CommitFile(t, "my-file", "1a\n", gittest.WithMessage("Commit 1a"))
		RequireAv(t, "stack", "branch", "stack-2")
		repo.CommitFile(t, "my-file", "1a\n2a\n", gittest.WithMessage("Commit 2a"))
		RequireAv(t, "stack", "branch", "stack-3")
		repo.CommitFile(
			t,
			"different-file",
			"1a\n2a\n3a\n",
			gittest.WithMessage("Commit 3a"),
		)

		currentBranch := repo.CurrentBranch(t)
		require.Equal(
			t,
			plumbing.ReferenceName("refs/heads/stack-3"),
			currentBranch,
			"stack-3 should be current branch",
		)

		// Make sure we've handled all the parent/child renames correctly
		db := repo.OpenDB(t)
		parentBranch := meta.LastChildOf(db.ReadTx(), currentBranch.Short())
		require.NotNil(t, parentBranch)
		require.Equal(t, "stack-3", parentBranch.Name)

		RequireAv(t, "stack", "switch", "stack-2")
		currentBranch = repo.CurrentBranch(t)
		parentBranch = meta.LastChildOf(db.ReadTx(), currentBranch.Short())
		require.NotNil(t, parentBranch)
		require.Equal(t, "stack-3", parentBranch.Name)

		RequireAv(t, "stack", "split", "stack-4")
		currentBranch = repo.CurrentBranch(t)
		require.Equal(t, "stack-4", currentBranch.Short())

	})
}
