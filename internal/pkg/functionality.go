package pkg

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/capnfabs/grouse/internal/checkout"
	"github.com/capnfabs/grouse/internal/out"
	"github.com/kballard/go-shellquote"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// TODO: get rid of this, put it into a dependency context or something.
var AppFs = afero.NewOsFs()

func version() string {
	if version, ok := os.LookupEnv("GROUSE_VERSION"); ok {
		return version
	}
	return "dev+" + time.Now().Format("2006-02-01T15:04:05")
}

var rootCmd = &cobra.Command{
	Use:     "grouse [flags] <commit> [<other-commit>]",
	Version: version(),
	Short:   "Diffs the output of a given Hugo git repo at different commits.",
	Long: `Diffs the output of a given Hugo git repo at different commits.

Imagine that on every commit of your Hugo site, you'd generated the site and
stored that in version control. Then, you could see exactly what's changed in
your generated site between different commits.

Grouse approximates that process.`,
	DisableFlagsInUseLine: true,
	Run: func(cmd *cobra.Command, args []string) {
		debug, err := cmd.Flags().GetBool("debug")
		check(err)
		out.Debug = debug

		context, err := parseArgs(cmd.Flags())
		if err != nil {
			out.Outln("Error:", err)
			cmd.Usage()
			os.Exit(1)
		}
		err = runMain(context)
		if err != nil {
			out.Outln("Error:", err)
			os.Exit(2)
		}
	},
}

func Main() {
	rootCmd.Flags().String("diffargs", "", "Arguments to pass on to 'git diff'")
	rootCmd.Flags().String("buildargs", "", "Arguments to pass on to the hugo build command")
	rootCmd.Flags().BoolP("tool", "t", false, "Invoke 'git difftool' instead of 'git diff'")
	rootCmd.Flags().Bool("debug", false, "Enables additional logging")
	if err := rootCmd.Execute(); err != nil {
		out.Outln(err)
		os.Exit(1)
	}
}

func commitAll(worktree *git.Worktree, msg string) (plumbing.Hash, error) {
	_, err := worktree.Add(".")
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return worktree.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Grouse Diff",
			Email: "grouse-diff@example.com",
			When:  time.Now(),
		},
	})
}

func runMain(context *cmdArgs) error {
	repo, err := git.PlainOpen(context.repoDir)
	if err != nil {
		// Should we return these errors instead of doing this?
		return errors.WithMessagef(err, "Couldn't load the git repo in %s", context.repoDir)
	}

	refs := []checkout.ResolvedCommit{}

	for _, commit := range context.commits {
		ref, err := checkout.ResolveUserRef(repo, commit)
		if err != nil {
			return errors.WithMessagef(err, "Couldn't resolve '%s'", ref)
		}
		refs = append(refs, ref)
	}

	out.Outf("Computing diff between revisions %s and %s\n", refs[0], refs[1])

	scratchDir, err := ioutil.TempDir("", "hugo_diff")
	// If this fails, we're unable to do anything with temp storage, so just
	// panic.
	check(err)

	// Init the Output Repo
	outputDir := path.Join(scratchDir, "output")
	outputRepo, err := git.PlainInit(outputDir, false)
	// Not the user's fault and nothing we can do; panicking is ok.
	check(err)

	outputWorktree, err := outputRepo.Worktree()
	// Shouldn't be possible; because this isn't a bare repo
	check(err)

	outputHashes := []plumbing.Hash{}

	for _, ref := range refs {
		// Make sure the output directory is empty
		err = eraseDirectoryExceptRootDotGit(outputDir)
		check(err)

		srcDir := path.Join(scratchDir, "source", ref.Hash().String())
		out.Outf("Building revision %s…\n", ref)
		hash, err := process(
			outputWorktree, ref, srcDir, outputDir, context.buildArgs)

		switch err.(type) {
		case *exec.ExitError:
			err := errors.Wrapf(err, "Building at commit %s failed", ref)
			return err
		case error:
			panic(err)
		}
		outputHashes = append(outputHashes, hash)
	}

	// Do the actual diff
	out.Outln("Diffing…")
	err = runDiff(outputDir, context.diffCommand, context.diffArgs, outputHashes[0], outputHashes[1])
	switch e := err.(type) {
	case *exec.ExitError:
		if strings.Contains(e.Error(), "signal: broken pipe") {
			// It's not an error; but the user exited 'less' or whatever
		} else {
			err := errors.Wrapf(
				err, "Running git %s failed", context.diffCommand)
			return err
		}
	case error:
		panic(err)
	}
	return nil
}

func eraseDirectoryExceptRootDotGit(directory string) error {
	infos, err := afero.ReadDir(AppFs, directory)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if info.Name() == ".git" {
			continue
		}

		err := AppFs.RemoveAll(path.Join(directory, info.Name()))
		if err != nil {
			return err
		}
	}
	return nil
}

func process(dstWorktree *git.Worktree, ref checkout.ResolvedCommit, hugoWorkingDir string, outputDir string, buildArgs []string) (plumbing.Hash, error) {
	out.Debugf("Checking out %s to %s…\n", ref, hugoWorkingDir)
	err := checkout.ExtractCommitToDirectory(ref, hugoWorkingDir)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	out.Debugln("…done checking out.")

	if err = runHugo(hugoWorkingDir, outputDir, buildArgs); err != nil {
		return plumbing.ZeroHash, err
	}

	commitMessage := fmt.Sprintf("Website content, built from %s", ref)
	hash, err := commitAll(dstWorktree, commitMessage)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return hash, nil
}

func runHugo(repoDir string, outputDir string, userArgs []string) error {
	// Put the 'destination' last. Repeated 'destination' flags only uses the
	// last one.
	// Note that we do it with the "--destination=/foo/" instead of "--destination foo"
	// because the former results in
	allArgs := append(userArgs, "--destination="+shellquote.Join(outputDir))
	cmd := exec.Command("hugo", allArgs...)
	out.Debugf("Running command %s\n", shellquote.Join(cmd.Args...))
	cmd.Dir = repoDir

	// TODO: if --debug is NOT specified, should hang on to these and then only
	// print them if an error occurs.
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	return cmd.Run()
}

func runDiff(repoDir, diffCommand string, userArgs []string, hash1, hash2 plumbing.Hash) error {
	allArgs := []string{diffCommand}
	allArgs = append(allArgs, userArgs...)
	allArgs = append(allArgs, hash1.String(), hash2.String())

	cmd := exec.Command("git", allArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Dir = repoDir
	out.Debugf("Running command %s\n", shellquote.Join(cmd.Args...))
	// This gets surfaced to the user because they're allowed to pass in diff
	// args, so it's probably (?) something they can fix?
	return cmd.Run()
}
