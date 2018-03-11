package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/juju/ansiterm"
	"github.com/urfave/cli"

	"github.com/manifoldco/torus-cli/api"
	"github.com/manifoldco/torus-cli/apitypes"
	"github.com/manifoldco/torus-cli/config"
	"github.com/manifoldco/torus-cli/envelope"
	"github.com/manifoldco/torus-cli/errs"
	"github.com/manifoldco/torus-cli/hints"
	"github.com/manifoldco/torus-cli/identity"
	"github.com/manifoldco/torus-cli/primitive"
	"github.com/manifoldco/torus-cli/prompts"
	"github.com/manifoldco/torus-cli/ui"
)

func init() {
	teams := cli.Command{
		Name:     "teams",
		Usage:    "Manage teams and their members",
		Category: "ACCESS CONTROL",
		Subcommands: []cli.Command{
			{
				Name:      "create",
				Usage:     "Create a team in an organization",
				ArgsUsage: "[name]",
				Flags: []cli.Flag{
					orgFlag("Create the team in this org", false),
				},
				Action: chain(
					ensureDaemon, ensureSession, loadDirPrefs, loadPrefDefaults,
					createTeamCmd,
				),
			},
			{
				Name:  "list",
				Usage: "List teams in an organization",
				Flags: []cli.Flag{
					orgFlag("Use this organization.", false),
				},
				Action: chain(
					ensureDaemon, ensureSession, loadDirPrefs, loadPrefDefaults,
					checkRequiredFlags, teamsListCmd,
				),
			},
			{
				Name:      "members",
				Usage:     "List members of a particular team in and organization",
				ArgsUsage: "<team>",
				Flags: []cli.Flag{
					orgFlag("Use this organization.", false),
				},
				Action: chain(
					ensureDaemon, ensureSession, loadDirPrefs, loadPrefDefaults,
					checkRequiredFlags, teamMembersListCmd,
				),
			},
			{
				Name:      "add",
				ArgsUsage: "<username> <team>",
				Usage:     "Add user to a specified team in an organization you administer",
				Flags: []cli.Flag{
					stdOrgFlag,
				},
				Action: chain(
					ensureDaemon, ensureSession, loadDirPrefs, loadPrefDefaults,
					checkRequiredFlags, teamsAddCmd,
				),
			},
			{
				Name:      "remove",
				Usage:     "Remove user from a specified team in an organization you administer",
				ArgsUsage: "<username> <team>",
				Flags: []cli.Flag{
					stdOrgFlag,
				},
				Action: chain(
					ensureDaemon, ensureSession, loadDirPrefs, loadPrefDefaults,
					checkRequiredFlags, teamsRemoveCmd,
				),
			},
		},
	}
	Cmds = append(Cmds, teams)
}

func teamsListCmd(ctx *cli.Context) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	client := api.NewClient(cfg)
	c := context.Background()

	org, _, _, err := selectOrg(c, client, ctx.String("org"), false)
	if err != nil {
		return err
	}

	var getMemberships, display sync.WaitGroup
	getMemberships.Add(1)
	display.Add(2)

	var teams []envelope.Team
	var session *api.Session
	var sErr, tErr error

	memberOf := make(map[identity.ID]bool)

	go func() {
		session, sErr = client.Session.Who(c)
		getMemberships.Done()
	}()

	go func() {
		teams, tErr = client.Teams.GetByOrg(c, org.ID)
		display.Done()
	}()

	go func() {
		getMemberships.Wait()
		var memberships []envelope.Membership
		if sErr == nil {
			memberships, sErr = client.Memberships.List(c, org.ID, nil, session.ID())
		}

		for _, m := range memberships {
			memberOf[*m.Body.TeamID] = true
		}
		display.Done()
	}()

	display.Wait()
	if sErr != nil || tErr != nil {
		return errs.MultiError(
			sErr,
			tErr,
			errs.NewExitError("Error fetching teams list"),
		)
	}

	numTeams := 0
	fmt.Println("")
	w := ansiterm.NewTabWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\t%s\t%s\n", ui.BoldString("Team"), ui.BoldString("Type"))
	for _, t := range teams {
		if isMachineTeam(t.Body) {
			continue
		}

		numTeams++

		isMember := ""
		displayTeamType := ""

		switch teamType := t.Body.TeamType; teamType {
		case primitive.SystemTeamType:
			displayTeamType = "system"
		case primitive.MachineTeamType:
			displayTeamType = "machine"
		case primitive.UserTeamType:
			displayTeamType = "user"
		}

		if _, ok := memberOf[*t.ID]; ok {
			isMember = ui.FaintString("*")
		}

		fmt.Fprintf(w, "%s\t%s\t%s\n", isMember, t.Body.Name, displayTeamType)
	}

	w.Flush()

	fmt.Printf("\nOrg %s has (%s) team%s\n", org.Body.Name,
		ui.FaintString(strconv.Itoa(numTeams)), plural(numTeams))

	return nil
}

func teamMembersListCmd(ctx *cli.Context) error {
	if err := argCheck(ctx, 1, 0); err != nil {
		return err
	}

	args := ctx.Args()

	teamName := ""
	if len(args) == 1 {
		teamName = args[0]
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	client := api.NewClient(cfg)
	c := context.Background()

	org, _, _, err := selectOrg(c, client, ctx.String("org"), false)
	if err != nil {
		return err
	}

	team, _, _, err := selectTeam(c, client, org, teamName, false)
	if err != nil {
		return err
	}

	var getMembers sync.WaitGroup
	getMembers.Add(2)

	var memberships []envelope.Membership
	var mErr, sErr error
	go func() {
		// Pull all memberships for supplied org/team
		memberships, mErr = client.Memberships.List(c, org.ID, team.ID, nil)
		getMembers.Done()
	}()

	var session *api.Session
	go func() {
		// Who am I
		session, sErr = client.Session.Who(c)
		getMembers.Done()
	}()

	getMembers.Wait()
	if mErr != nil || sErr != nil {
		return errs.MultiError(mErr, sErr)
	}

	if len(memberships) == 0 {
		fmt.Printf("%s has no members\n", team.Body.Name)
		return nil
	}

	membershipUserIDs := make(map[identity.ID]bool)
	for _, membership := range memberships {
		membershipUserIDs[*membership.Body.OwnerID] = true
	}

	var profileIDs []identity.ID
	for id := range membershipUserIDs {
		profileIDs = append(profileIDs, id)
	}

	profiles, err := client.Profiles.ListByID(c, profileIDs)
	if err != nil {
		return err
	}
	if profiles == nil {
		return errs.NewExitError("User not found.")
	}

	fmt.Println("")
	w := ansiterm.NewTabWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\t%s\t%s\n", ui.BoldString("Name"), ui.BoldString("Username"))
	for _, profile := range profiles {
		me := ""
		if session.Username() == profile.Body.Username {
			me = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", me, profile.Body.Name, ui.FaintString(profile.Body.Username))
	}

	w.Flush()

	fmt.Printf("\nTeam %s has (%s) member%s\n", team.Body.Name,
		ui.FaintString(strconv.Itoa(len(memberships))), plural(len(memberships)))

	return nil
}

const teamCreateFailed = "Could not create team."

func createTeamCmd(ctx *cli.Context) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return errs.NewErrorExitError(teamCreateFailed, err)
	}

	if err := argCheck(ctx, 1, 0); err != nil {
		return err
	}

	args := ctx.Args()
	teamName := ""
	if len(args) > 0 {
		teamName = args[0]
	}

	client := api.NewClient(cfg)
	c := context.Background()

	org, _, _, err := selectOrg(c, client, ctx.String("org"), false)
	if err != nil {
		return err
	}

	teamName, err = prompts.TeamName(teamName, false)
	if err != nil {
		return err
	}

	// Create our new team
	fmt.Println("")
	_, err = client.Teams.Create(c, org.ID, teamName, primitive.UserTeamType)
	if err != nil {
		if strings.Contains(err.Error(), "resource exists") {
			return errs.NewExitError("Team already exists")
		}
		return errs.NewErrorExitError(teamCreateFailed, err)
	}

	fmt.Printf("Team %s created.\n", teamName)

	hints.Display(hints.Allow, hints.Deny)
	return nil
}

const teamRemoveFailed = "Failed to remove team member."

func teamsRemoveCmd(ctx *cli.Context) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	if err := argCheck(ctx, 2, 2); err != nil {
		return err
	}

	args := ctx.Args()
	username := args[0]
	teamName := args[1]

	client := api.NewClient(cfg)
	c := context.Background()

	var wait sync.WaitGroup
	wait.Add(2)

	var uErr, oErr, tErr error
	var org *envelope.Org
	var team envelope.Team
	var user *apitypes.Profile

	go func() {
		// Identify the org supplied
		result, err := client.Orgs.GetByName(c, ctx.String("org"))
		if result == nil || err != nil {
			oErr = errs.NewExitError("Org not found.")
			wait.Done()
			return
		}
		org = result

		// Retrieve the team by name supplied
		results, err := client.Teams.GetByName(c, org.ID, teamName)
		if len(results) != 1 || err != nil {
			tErr = errs.NewExitError("Team not found.")
		} else {
			team = results[0]
		}
		wait.Done()
	}()

	go func() {
		// Retrieve the user by name supplied
		result, err := client.Profiles.ListByName(c, username)
		if result == nil || err != nil {
			uErr = errs.NewExitError("User not found.")
		} else {
			user = result
		}
		wait.Done()
	}()

	wait.Wait()
	if uErr != nil || oErr != nil || tErr != nil {
		return errs.MultiError(
			oErr,
			uErr,
			tErr,
		)
	}

	preamble := fmt.Sprintf("You are about to remove %s from the %s team in the %s org.",
		ui.FaintString(user.Body.Username), team.Body.Name, org.Body.Name)
	success, err := prompts.Confirm(nil, &preamble, true, false)
	if err != nil {
		return errs.NewErrorExitError("Failed to retrieve confirmation", err)
	}
	if !success {
		return errs.ErrAbort
	}

	// Lookup their membership row
	memberships, mErr := client.Memberships.List(c, org.ID, team.ID, user.ID)
	if mErr != nil || len(memberships) < 1 {
		return errs.NewExitError("Memberships not found.")
	}

	err = client.Memberships.Delete(c, memberships[0].ID)
	if err != nil {
		msg := teamRemoveFailed
		if strings.Contains(err.Error(), "member of the") {
			msg = "Must be a member of the admin team to remove members"
		}
		if strings.Contains(err.Error(), "cannot remove") {
			msg = "Cannot remove members from the member team"
		}
		return errs.NewExitError(msg)
	}

	fmt.Println(username + " has been removed from " + teamName + " team")
	return nil
}

const teamAddFailed = "Failed to add team member, please try again"

func teamsAddCmd(ctx *cli.Context) error {
	if err := argCheck(ctx, 2, 2); err != nil {
		return err
	}

	args := ctx.Args()
	username := args[0]
	teamName := args[1]

	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	client := api.NewClient(cfg)
	c := context.Background()

	var wait sync.WaitGroup
	wait.Add(2)

	var uErr, oErr, tErr error
	var org *envelope.Org
	var team envelope.Team
	var user *apitypes.Profile

	go func() {
		// Identify the org supplied
		result, err := client.Orgs.GetByName(c, ctx.String("org"))
		if result == nil || err != nil {
			oErr = errs.NewExitError("Org not found.")
			wait.Done()
			return
		}
		org = result

		// Retrieve the team by name supplied
		results, err := client.Teams.GetByName(c, org.ID, teamName)
		if len(results) != 1 || err != nil {
			tErr = errs.NewExitError("Team not found.")
			wait.Done()
			return
		}
		team = results[0]
		wait.Done()
	}()

	go func() {
		// Retrieve the user by name supplied
		result, err := client.Profiles.ListByName(c, username)
		if result == nil || err != nil {
			uErr = errs.NewExitError("User not found.")
		} else {
			user = result
		}
		wait.Done()
	}()

	wait.Wait()
	if uErr != nil || oErr != nil || tErr != nil {
		return errs.MultiError(
			oErr,
			uErr,
			tErr,
		)
	}

	err = client.Memberships.Create(c, user.ID, org.ID, team.ID)
	if err != nil {
		msg := teamAddFailed
		if strings.Contains(err.Error(), "member of the") {
			msg = "Must be a member of the admin team to add members."
		}
		if strings.Contains(err.Error(), "resource exists") {
			msg = username + " is already a member of the " + teamName + " team."
		}
		if strings.Contains(err.Error(), "to the members team") {
			msg = username + " cannot be added to the " + teamName + " team."
		}
		return errs.NewExitError(msg)
	}

	fmt.Println(username + " has been added to the " + teamName + " team.")
	return nil
}

// isMachineTeam returns whether or not the given team represents a machine
// role (which uses the Team primitive)
func isMachineTeam(team *primitive.Team) bool {
	return team.TeamType == primitive.MachineTeamType || (team.TeamType == primitive.SystemTeamType && team.Name == primitive.MachineTeamName)
}
