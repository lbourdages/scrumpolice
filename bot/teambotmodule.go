package bot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nlopes/slack"
	log "github.com/sirupsen/logrus"
	"github.com/pastjean/scrumpolice/scrum"
	"time"
	"github.com/robfig/cron"
)

// HandleMessage handle a received message for team and returns if the bot shall continue to process the message or stop
// continue = true
// stop = false
func (b *Bot) HandleTeamEditionMessage(event *slack.MessageEvent) bool {
	// "edit team [name]"
	// [name == name of the team]
	// editing team [name]. if you want to abort say stop scrum

	if context, ok := b.userContexts[event.User]; ok {
		return context.HandleMessage(event)
	}

	if strings.HasPrefix(strings.ToLower(event.Text), "edit team") {
		return b.startTeamEdition(event)
	}

	if strings.HasPrefix(strings.ToLower(event.Text), "add team") {
		return b.startTeamCreation(event)
	}

	if strings.HasPrefix(strings.ToLower(event.Text), "remove team") {
		return b.startTeamDeletion(event)
	}

	return true
}

func (b *Bot) cancelTeamEdition(event *slack.MessageEvent) bool {
	_, err := b.slackBotAPI.GetUserInfo(event.User)
	if err != nil {
		b.logSlackRelatedError(event, err, "Fail to get user information.")
		return false
	}

	b.slackBotAPI.PostMessage(event.Channel, "Team edition was cancelled. Better luck next time!", slack.PostMessageParameters{AsUser: true})
	return false
}

func (b *Bot) startTeamDeletion(event *slack.MessageEvent) bool {
	_, err := b.slackBotAPI.GetUserInfo(event.User)
	if err != nil {
		b.logSlackRelatedError(event, err, "Fail to get user information.")
		b.slackBotAPI.PostMessage(event.Channel, "There was an error editing the team, please try again", slack.PostMessageParameters{AsUser: true})
		return false
	}

	return b.chooseTeamToEdit(event, func (event *slack.MessageEvent, team string) bool {
		return b.chosenTeamToDelete(event, team)
	})
}

func (b *Bot) chosenTeamToDelete(event *slack.MessageEvent, team string) bool {
	expected := "remove team "+team
	msg := "Type `"+expected+"` to delete the team or type `quit`"
	b.slackBotAPI.PostMessage(event.Channel, msg, slack.PostMessageParameters{AsUser: true})

	b.setUserContext(event.User, b.canQuitBotContextHandlerFunc(func(event *slack.MessageEvent) bool {
		if event.Text == expected {
			author, err := b.slackBotAPI.GetUserInfo(event.User)
			if err != nil {
				b.logSlackRelatedError(event, err, "Fail to get user information.")
				return false
			}

			b.scrum.DeleteTeam(team)

			b.slackBotAPI.PostMessage(event.Channel, "I've deleted the team "+team, slack.PostMessageParameters{AsUser: true})
			log.WithFields(log.Fields{
				"team":   team,
				"doneBy": author.Name,
			}).Info("Team was deleted.")

			b.unsetUserContext(event.User)
			return false
		}

		return b.chosenTeamToDelete(event, team)
	}))

	return false
}


func (b *Bot) startTeamCreation(event *slack.MessageEvent) bool {
	_, err := b.slackBotAPI.GetUserInfo(event.User)
	if err != nil {
		b.logSlackRelatedError(event, err, "Fail to get user information.")
		b.slackBotAPI.PostMessage(event.Channel, "There was an error editing the team, please try again", slack.PostMessageParameters{AsUser: true})
		return false
	}

	return b.chooseTeamName(event)
}

func (b *Bot) chooseTeamName(event *slack.MessageEvent) bool {
	msg := "What should be the team name?"
	b.slackBotAPI.PostMessage(event.Channel, msg, slack.PostMessageParameters{AsUser: true})

	b.setUserContext(event.User, b.canQuitBotContextHandlerFunc(func(event *slack.MessageEvent) bool {
		teams := b.scrum.GetTeams()
		newTeamName := event.Text

		for _, team := range teams {
			if team == newTeamName {
				b.slackBotAPI.PostMessage(event.Channel, "Team already exists, choose a new name or type `quit`", slack.PostMessageParameters{AsUser: true})
				b.chooseTeamName(event)
				return false
			}
		}

		author, err := b.slackBotAPI.GetUserInfo(event.User)
		if err != nil {
			b.logSlackRelatedError(event, err, "Fail to get user information.")
			return false
		}

		members := []string{author.Name}
		var firstReminderBefore time.Duration = -8 * time.Second
		var lastReminderBefore time.Duration = -8 * time.Second
		schedule, _ := cron.Parse("@every 30s")

		questions := []*scrum.QuestionSet{&scrum.QuestionSet{
			Questions: []string{"What did you do yesterday?", "What will you do today?", "Are you being blocked by someone for a review? who? why?"},
			FirstReminderBeforeReport: firstReminderBefore,
			LastReminderBeforeReport: lastReminderBefore,
			ReportScheduleCron: "@every 30s",
			ReportSchedule: schedule,
		}}

		b.scrum.AddTeam(&scrum.Team{
			Name: newTeamName,
			Channel: "@"+author.Name,
			Members: members,
			SplitReport: true,
			OutOfOffice: []string{},
			QuestionsSets: questions,
		})

		return b.choosenTeamToEdit(event, newTeamName)
	}))
	return false
}

func (b *Bot) startTeamEdition(event *slack.MessageEvent) bool {
	_, err := b.slackBotAPI.GetUserInfo(event.User)
	if err != nil {
		b.logSlackRelatedError(event, err, "Fail to get user information.")
		b.slackBotAPI.PostMessage(event.Channel, "There was an error editing the team, please try again", slack.PostMessageParameters{AsUser: true})
		return false
	}

	return b.chooseTeamToEdit(event, func (event *slack.MessageEvent, team string) bool {
		return b.choosenTeamToEdit(event, team)
	})
}

type ChosenTeamFunc func(event *slack.MessageEvent, team string) bool

func (b *Bot) chooseTeamToEdit(event *slack.MessageEvent, cb ChosenTeamFunc) bool {
	user, err := b.slackBotAPI.GetUserInfo(event.User)
	if err != nil {
		b.logSlackRelatedError(event, err, "Fail to get user information.")
		return false
	}

	teams := b.scrum.GetTeamsForUser(user.Name)
	if len(teams) == 0 {
		b.slackBotAPI.PostMessage(event.Channel, "There is no teams, use 'create team' to create a new team", slack.PostMessageParameters{AsUser: true})
		return false
	}

	choices := make([]string, len(teams))
	sort.Strings(teams)
	for i, team := range teams {
		choices[i] = fmt.Sprintf("%d - %s", i, team)
	}

	msg := fmt.Sprintf("Choose a team :\n%s", strings.Join(choices, "\n"))
	b.slackBotAPI.PostMessage(event.Channel, msg, slack.PostMessageParameters{AsUser: true})

	b.setUserContext(event.User, b.canQuitBotContextHandlerFunc(func(event *slack.MessageEvent) bool {
		i, err := strconv.Atoi(event.Text)

		if i < 0 || i >= len(teams) || err != nil {
			b.slackBotAPI.PostMessage(event.Channel, "Wrong choices, please try again :p or type `quit`", slack.PostMessageParameters{AsUser: true})
			b.chooseTeamToEdit(event, cb)
			return false
		}

		return cb(event, teams[i])
	}))

	return false
}

func (b *Bot) choosenTeamToEdit(event *slack.MessageEvent, team string) bool {
	message := slack.Attachment{
		MarkdownIn: []string{"text"},
		Text: "" +
			"- `add @name`: Add *@name* to team\n" +
			"- `remove @name`: Remove *@name* from team",
	}

	params := slack.PostMessageParameters{AsUser: true}
	params.Attachments = []slack.Attachment{message}

	_, _, err := b.slackBotAPI.PostMessage(event.Channel, "What do you want to do with team"+team, params)
	if err != nil {
		b.logSlackRelatedError(event, err, "Fail to post message to slack.")
		return false
	}

	b.setUserContext(event.User, b.canQuitBotContextHandlerFunc(func(event *slack.MessageEvent) bool {
		params := getParams(`(?i)(?P<action>add|remove) <@(?P<user>.+)>\s*`, event.Text)
		fmt.Println(params)

		if len(params) == 0 || params["action"] == "" || params["user"] == "" {
			b.slackBotAPI.PostMessage(event.Channel, "Wrong choices, please try again :p or type `quit`", slack.PostMessageParameters{AsUser: true})
			b.choosenTeamToEdit(event, team)
			return false
		}
		userId := params["user"]
		b.logSlackRelatedError(event, err, "Fail to get user information. user:"+userId)

		user, err := b.slackBotAPI.GetUserInfo(userId)
		if err != nil {
			b.logSlackRelatedError(event, err, "Fail to get user information.")
			b.slackBotAPI.PostMessage(event.Channel, "Hmmmm, I couldn't find the user. Try again!", slack.PostMessageParameters{AsUser: true})
			b.choosenTeamToEdit(event, team)
			return false
		}
		username := user.Name

		return b.chooseTeamAction(event, team, params["action"], username)
	}))

	return false
}

func (b *Bot) chooseTeamAction(event *slack.MessageEvent, team string, action string, username string) bool {
	if action == "add" {
		b.scrum.AddToTeam(team, username)

		b.slackBotAPI.PostMessage(event.Channel, "I've added @"+username+" to team "+team, slack.PostMessageParameters{AsUser: true})

		author, err := b.slackBotAPI.GetUserInfo(event.User)
		if err != nil {
			b.logSlackRelatedError(event, err, "Fail to get user information.")
			return false
		}

		b.slackBotAPI.PostMessage("@"+username, "You've been added in team "+team+" by @"+author.Name+".", slack.PostMessageParameters{AsUser: true})
		log.WithFields(log.Fields{
			"user":   username,
			"team":   team,
			"doneBy": author.Name,
		}).Info("User was added to team.")
	} else if action == "remove" {
		b.scrum.RemoveFromTeam(team, username)

		b.slackBotAPI.PostMessage(event.Channel, "I've removed @"+username+" to team "+team, slack.PostMessageParameters{AsUser: true})

		author, err := b.slackBotAPI.GetUserInfo(event.User)
		if err != nil {
			b.logSlackRelatedError(event, err, "Fail to get user information.")
			return false
		}

		b.slackBotAPI.PostMessage("@"+username, "You've been removed from team "+team+" by @"+author.Name+".", slack.PostMessageParameters{AsUser: true})
		log.WithFields(log.Fields{
			"user":   username,
			"team":   team,
			"doneBy": author.Name,
		}).Info("User was removed from team.")
	}

	b.unsetUserContext(event.User)
	return false
}
