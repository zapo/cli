package command

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/cli/cli/api"
	"github.com/cli/cli/context"
	"github.com/cli/cli/git"
	"github.com/cli/cli/internal/ghrepo"
	"github.com/cli/cli/pkg/text"
	"github.com/cli/cli/utils"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	RootCmd.AddCommand(prCmd)
	prCmd.AddCommand(prCheckoutCmd)
	prCmd.AddCommand(prCreateCmd)
	prCmd.AddCommand(prListCmd)
	prCmd.AddCommand(prStatusCmd)
	prCmd.AddCommand(prViewCmd)

	prListCmd.Flags().IntP("limit", "L", 30, "Maximum number of items to fetch")
	prListCmd.Flags().StringP("state", "s", "open", "Filter by state: {open|closed|merged|all}")
	prListCmd.Flags().StringP("base", "B", "", "Filter by base branch")
	prListCmd.Flags().StringSliceP("label", "l", nil, "Filter by label")
	prListCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")

	prViewCmd.Flags().BoolP("preview", "p", false, "Display preview of pull request content")
}

var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Create, view, and checkout pull requests",
	Long: `Work with GitHub pull requests.

A pull request can be supplied as argument in any of the following formats:
- by number, e.g. "123";
- by URL, e.g. "https://github.com/OWNER/REPO/pull/123"; or
- by the name of its head branch, e.g. "patch-1" or "OWNER:patch-1".`,
}
var prListCmd = &cobra.Command{
	Use:   "list",
	Short: "List and filter pull requests in this repository",
	RunE:  prList,
}
var prStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of relevant pull requests",
	RunE:  prStatus,
}
var prViewCmd = &cobra.Command{
	Use:   "view [{<number> | <url> | <branch>}]",
	Short: "View a pull request in the browser",
	Long: `View a pull request specified by the argument in the browser.

Without an argument, the pull request that belongs to the current
branch is opened.`,
	RunE: prView,
}

func prStatus(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)

	palette, err := utils.NewPalette(cmd)
	if err != nil {
		return err
	}

	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	currentPRNumber, currentPRHeadRef, err := prSelectorForCurrentBranch(ctx)
	if err != nil {
		return err
	}
	currentUser, err := ctx.AuthLogin()
	if err != nil {
		return err
	}

	baseRepo, err := determineBaseRepo(cmd, ctx)
	if err != nil {
		return err
	}

	prPayload, err := api.PullRequests(apiClient, baseRepo, currentPRNumber, currentPRHeadRef, currentUser)
	if err != nil {
		return err
	}

	out := colorableOut(cmd)

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "Relevant pull requests in %s\n", ghrepo.FullName(baseRepo))
	fmt.Fprintln(out, "")

	printHeader(out, palette, "Current branch")
	if prPayload.CurrentPRs != nil {
		printPrs(out, palette, 0, prPayload.CurrentPRs...)
	} else {
		message := fmt.Sprintf("  There is no pull request associated with %s", palette.Cyan("["+currentPRHeadRef+"]"))
		printMessage(out, palette, message)
	}
	fmt.Fprintln(out)

	printHeader(out, palette, "Created by you")
	if prPayload.ViewerCreated.TotalCount > 0 {
		printPrs(out, palette, prPayload.ViewerCreated.TotalCount, prPayload.ViewerCreated.PullRequests...)
	} else {
		printMessage(out, palette, "  You have no open pull requests")
	}
	fmt.Fprintln(out)

	printHeader(out, palette, "Requesting a code review from you")
	if prPayload.ReviewRequested.TotalCount > 0 {
		printPrs(out, palette, prPayload.ReviewRequested.TotalCount, prPayload.ReviewRequested.PullRequests...)
	} else {
		printMessage(out, palette, "  You have no pull requests to review")
	}
	fmt.Fprintln(out)

	return nil
}

func prList(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)

	palette, err := utils.NewPalette(cmd)
	if err != nil {
		return err
	}

	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	baseRepo, err := determineBaseRepo(cmd, ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(colorableErr(cmd), "\nPull requests for %s\n\n", ghrepo.FullName(baseRepo))

	limit, err := cmd.Flags().GetInt("limit")
	if err != nil {
		return err
	}
	state, err := cmd.Flags().GetString("state")
	if err != nil {
		return err
	}
	baseBranch, err := cmd.Flags().GetString("base")
	if err != nil {
		return err
	}
	labels, err := cmd.Flags().GetStringSlice("label")
	if err != nil {
		return err
	}
	assignee, err := cmd.Flags().GetString("assignee")
	if err != nil {
		return err
	}

	var graphqlState []string
	switch state {
	case "open":
		graphqlState = []string{"OPEN"}
	case "closed":
		graphqlState = []string{"CLOSED", "MERGED"}
	case "merged":
		graphqlState = []string{"MERGED"}
	case "all":
		graphqlState = []string{"OPEN", "CLOSED", "MERGED"}
	default:
		return fmt.Errorf("invalid state: %s", state)
	}

	params := map[string]interface{}{
		"owner": baseRepo.RepoOwner(),
		"repo":  baseRepo.RepoName(),
		"state": graphqlState,
	}
	if len(labels) > 0 {
		params["labels"] = labels
	}
	if baseBranch != "" {
		params["baseBranch"] = baseBranch
	}
	if assignee != "" {
		params["assignee"] = assignee
	}

	prs, err := api.PullRequestList(apiClient, params, limit)
	if err != nil {
		return err
	}

	if len(prs) == 0 {
		colorErr := colorableErr(cmd) // Send to stderr because otherwise when piping this command it would seem like the "no open prs" message is actually a pr
		msg := "There are no open pull requests"

		userSetFlags := false
		cmd.Flags().Visit(func(f *pflag.Flag) {
			userSetFlags = true
		})
		if userSetFlags {
			msg = "No pull requests match your search"
		}
		printMessage(colorErr, palette, msg)
		return nil
	}

	table := utils.NewTablePrinter(cmd.OutOrStdout())
	for _, pr := range prs {
		prNum := strconv.Itoa(pr.Number)
		if table.IsTTY() {
			prNum = "#" + prNum
		}
		table.AddField(prNum, nil, colorFuncForPR(pr, palette))
		table.AddField(replaceExcessiveWhitespace(pr.Title), nil, nil)
		table.AddField(pr.HeadLabel(), nil, palette.Cyan)
		table.EndRow()
	}
	err = table.Render()
	if err != nil {
		return err
	}

	return nil
}

func colorFuncForPR(pr api.PullRequest, palette *utils.Palette) func(string) string {
	if pr.State == "OPEN" && pr.IsDraft {
		return palette.Gray
	} else {
		return colorFuncForState(pr.State, palette)
	}
}

func colorFuncForState(state string, palette *utils.Palette) func(string) string {
	switch state {
	case "OPEN":
		return palette.Green
	case "CLOSED":
		return palette.Red
	case "MERGED":
		return palette.Magenta
	default:
		return nil
	}
}

func prView(cmd *cobra.Command, args []string) error {
	ctx := contextForCommand(cmd)

	palette, err := utils.NewPalette(cmd)
	if err != nil {
		return err
	}

	apiClient, err := apiClientForContext(ctx)
	if err != nil {
		return err
	}

	baseRepo, err := determineBaseRepo(cmd, ctx)
	if err != nil {
		return err
	}

	preview, err := cmd.Flags().GetBool("preview")
	if err != nil {
		return err
	}

	var openURL string
	var pr *api.PullRequest
	if len(args) > 0 {
		pr, err = prFromArg(apiClient, baseRepo, args[0])
		if err != nil {
			return err
		}
		openURL = pr.URL
	} else {
		prNumber, branchWithOwner, err := prSelectorForCurrentBranch(ctx)
		if err != nil {
			return err
		}

		if prNumber > 0 {
			openURL = fmt.Sprintf("https://github.com/%s/pull/%d", ghrepo.FullName(baseRepo), prNumber)
			if preview {
				pr, err = api.PullRequestByNumber(apiClient, baseRepo, prNumber)
				if err != nil {
					return err
				}
			}
		} else {
			pr, err = api.PullRequestForBranch(apiClient, baseRepo, branchWithOwner)
			if err != nil {
				return err
			}

			openURL = pr.URL
		}
	}

	if preview {
		out := colorableOut(cmd)
		return printPrPreview(out, palette, pr)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "Opening %s in your browser.\n", openURL)
		return utils.OpenInBrowser(openURL)
	}
}

func printPrPreview(out io.Writer, palette *utils.Palette, pr *api.PullRequest) error {
	fmt.Fprintln(out, palette.Bold(pr.Title))
	fmt.Fprintln(out, palette.Gray(fmt.Sprintf(
		"%s wants to merge %s into %s from %s",
		pr.Author.Login,
		utils.Pluralize(pr.Commits.TotalCount, "commit"),
		pr.BaseRefName,
		pr.HeadRefName,
	)))
	if pr.Body != "" {
		fmt.Fprintln(out)
		md, err := utils.RenderMarkdown(pr.Body)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, md)
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, palette.Gray("View this pull request on GitHub: %s\n"), pr.URL)
	return nil
}

var prURLRE = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

func prFromArg(apiClient *api.Client, baseRepo ghrepo.Interface, arg string) (*api.PullRequest, error) {
	if prNumber, err := strconv.Atoi(strings.TrimPrefix(arg, "#")); err == nil {
		return api.PullRequestByNumber(apiClient, baseRepo, prNumber)
	}

	if m := prURLRE.FindStringSubmatch(arg); m != nil {
		prNumber, _ := strconv.Atoi(m[3])
		return api.PullRequestByNumber(apiClient, baseRepo, prNumber)
	}

	return api.PullRequestForBranch(apiClient, baseRepo, arg)
}

func prSelectorForCurrentBranch(ctx context.Context) (prNumber int, prHeadRef string, err error) {
	baseRepo, err := ctx.BaseRepo()
	if err != nil {
		return
	}
	prHeadRef, err = ctx.Branch()
	if err != nil {
		return
	}
	branchConfig := git.ReadBranchConfig(prHeadRef)

	// the branch is configured to merge a special PR head ref
	prHeadRE := regexp.MustCompile(`^refs/pull/(\d+)/head$`)
	if m := prHeadRE.FindStringSubmatch(branchConfig.MergeRef); m != nil {
		prNumber, _ = strconv.Atoi(m[1])
		return
	}

	var branchOwner string
	if branchConfig.RemoteURL != nil {
		// the branch merges from a remote specified by URL
		if r, err := ghrepo.FromURL(branchConfig.RemoteURL); err == nil {
			branchOwner = r.RepoOwner()
		}
	} else if branchConfig.RemoteName != "" {
		// the branch merges from a remote specified by name
		rem, _ := ctx.Remotes()
		if r, err := rem.FindByName(branchConfig.RemoteName); err == nil {
			branchOwner = r.RepoOwner()
		}
	}

	if branchOwner != "" {
		if strings.HasPrefix(branchConfig.MergeRef, "refs/heads/") {
			prHeadRef = strings.TrimPrefix(branchConfig.MergeRef, "refs/heads/")
		}
		// prepend `OWNER:` if this branch is pushed to a fork
		if !strings.EqualFold(branchOwner, baseRepo.RepoOwner()) {
			prHeadRef = fmt.Sprintf("%s:%s", branchOwner, prHeadRef)
		}
	}

	return
}

func printPrs(w io.Writer, palette *utils.Palette, totalCount int, prs ...api.PullRequest) {
	for _, pr := range prs {
		prNumber := fmt.Sprintf("#%d", pr.Number)

		prNumberColorFunc := palette.Green
		if pr.IsDraft {
			prNumberColorFunc = palette.Gray
		} else if pr.State == "MERGED" {
			prNumberColorFunc = palette.Magenta
		} else if pr.State == "CLOSED" {
			prNumberColorFunc = palette.Red
		}

		fmt.Fprintf(w, "  %s  %s %s", prNumberColorFunc(prNumber), text.Truncate(50, replaceExcessiveWhitespace(pr.Title)), palette.Cyan("["+pr.HeadLabel()+"]"))

		checks := pr.ChecksStatus()
		reviews := pr.ReviewStatus()
		if checks.Total > 0 || reviews.ChangesRequested || reviews.Approved {
			fmt.Fprintf(w, "\n  ")
		}

		if checks.Total > 0 {
			var summary string
			if checks.Failing > 0 {
				if checks.Failing == checks.Total {
					summary = palette.Red("All checks failing")
				} else {
					summary = palette.Red(fmt.Sprintf("%d/%d checks failing", checks.Failing, checks.Total))
				}
			} else if checks.Pending > 0 {
				summary = palette.Yellow("Checks pending")
			} else if checks.Passing == checks.Total {
				summary = palette.Green("Checks passing")
			}
			fmt.Fprintf(w, " - %s", summary)
		}

		if reviews.ChangesRequested {
			fmt.Fprintf(w, " - %s", palette.Red("Changes requested"))
		} else if reviews.ReviewRequired {
			fmt.Fprintf(w, " - %s", palette.Yellow("Review required"))
		} else if reviews.Approved {
			fmt.Fprintf(w, " - %s", palette.Green("Approved"))
		}

		fmt.Fprint(w, "\n")
	}
	remaining := totalCount - len(prs)
	if remaining > 0 {
		fmt.Fprintf(w, palette.Gray("  And %d more\n"), remaining)
	}
}

func printHeader(w io.Writer, palette *utils.Palette, s string) {
	fmt.Fprintln(w, palette.Bold(s))
}

func printMessage(w io.Writer, palette *utils.Palette, s string) {
	fmt.Fprintln(w, palette.Gray(s))
}

func replaceExcessiveWhitespace(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\r?\n`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\s{2,}`).ReplaceAllString(s, " ")
	return s
}
