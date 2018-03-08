package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/GoodCodingFriends/gpay/config"
	"github.com/GoodCodingFriends/gpay/entity"
	"github.com/GoodCodingFriends/gpay/repository"
	"github.com/GoodCodingFriends/gpay/usecase"
	"github.com/nlopes/slack"
)

const (
	cmdTypePay     = "pay"
	cmdTypeClaim   = "claim"
	cmdTypeBalance = "balance"
)

var (
	ErrNotGPAYCommand = errors.New("not gpay command, ignore")

	ErrUnknownCommand = errors.New("unknown command")
	ErrInvalidUsage   = errors.New("invalid usage")
	ErrInvalidUserID  = errors.New("invalid user id")

	userIDPattern = regexp.MustCompile(`^<@(.*)>$`)
)

type SlackBot struct {
	logger *log.Logger
	cfg    *config.Config
	client *slack.Client
	repo   *repository.Repository

	// for testing
	disableAPIRequest bool

	// TODO: lock
	idToSlackUser map[string]slack.User
}

func NewSlackBot(logger *log.Logger, cfg *config.Config, repo *repository.Repository) (*SlackBot, error) {
	client := slack.New(cfg.Controller.Slack.APIToken)
	slack.SetLogger(logger)
	bot := &SlackBot{
		logger: logger,
		cfg:    cfg,
		client: client,
		repo:   repo,
	}
	return bot, bot.updateSlackUsers()
}

func (b *SlackBot) Listen() error {
	rtm := b.client.NewRTM()
	go rtm.ManageConnection()
	for m := range rtm.IncomingEvents {
		switch e := m.Data.(type) {
		case *slack.MessageEvent:
			err := b.handleMessageEvent(e)
			if err == ErrNotGPAYCommand {
				continue
			}

			if err != nil {
				b.logger.Printf("handleMessageEvent: %s", err)
				if msg, ok := errToSlackMessage[err]; ok {
					b.postMessage(e, msg)
				}
				continue
			}
		}
	}
	return nil
}

func (b *SlackBot) Stop() error {
	return nil
}

func (b *SlackBot) updateSlackUsers() error {
	b.idToSlackUser = map[string]slack.User{}
	if b.disableAPIRequest {
		return nil
	}

	users, err := b.client.GetUsers()
	if err != nil {
		return err
	}
	for _, u := range users {
		id := u.ID
		if id == "" || u.IsBot {
			continue
		}
		b.idToSlackUser[id] = u
	}
	return nil
}

// handleMessageEvent validates the message text and handles the event
// valid formats are like this:
//
//   @gpay send 500 @ktr
//   @gpay send @ktr 500
//   @gpay claim 500 @ktr
//   @gpay claim @ktr 500
//
func (b *SlackBot) handleMessageEvent(e *slack.MessageEvent) error {
	if !strings.HasPrefix(e.Text, b.cfg.Controller.Slack.BotName) {
		b.logger.Println("not gpay command, ignore")
		return ErrNotGPAYCommand
	}

	sp := strings.Split(e.Text, " ")
	if len(sp) < 2 {
		// show usage
		return ErrInvalidUsage
	}

	cmdType := sp[1]
	from := entity.UserID(e.User)

	switch cmdType {
	case cmdTypePay:
		if len(sp) != 4 {
			return ErrInvalidUsage
		}
		return b.handlePayCommand(e, from, sp[2:])
	case cmdTypeClaim:
		if len(sp) != 4 {
			return ErrInvalidUsage
		}
		return b.handleClaimCommand(e, from, sp[2:])
	case cmdTypeBalance:
		return b.handleBalanceCommand(e, from)
	default:
		return ErrUnknownCommand
	}
}

func (b *SlackBot) handlePayCommand(e *slack.MessageEvent, fromID entity.UserID, sp []string) error {
	p := &parser{idToSlackUser: b.idToSlackUser}
	toID, amount, err := p.parse(sp)
	if err != nil {
		return err
	}
	from, to, err := usecase.FindBothUsersWithUserCreation(b.cfg, b.repo, fromID, toID)
	tx, err := usecase.Pay(b.repo, &usecase.PayParam{
		From:    from,
		To:      to,
		Amount:  amount,
		Message: "",
	})
	if err != nil {
		return err
	}
	b.logger.Printf("%#v\n", tx)
	b.addDoneReaction(e)
	return nil
}

func (b *SlackBot) handleClaimCommand(e *slack.MessageEvent, fromID entity.UserID, sp []string) error {
	p := &parser{idToSlackUser: b.idToSlackUser}
	toID, amount, err := p.parse(sp)
	if err != nil {
		return err
	}
	from, to, err := usecase.FindBothUsersWithUserCreation(b.cfg, b.repo, fromID, toID)
	invoice, err := usecase.Claim(b.repo, &usecase.ClaimParam{
		From:    from,
		To:      to,
		Amount:  amount,
		Message: "",
	})
	if err != nil {
		return err
	}

	msg := fmt.Sprintf("<@%s>", to.ID)
	btns := slack.Attachment{
		Text: fmt.Sprintf(claimMessage, from.ID, amount),
		Actions: []slack.AttachmentAction{
			{
				Name:  "accept",
				Type:  "button",
				Text:  "支払う",
				Style: "primary",
			},
			{
				Name:  "reject",
				Type:  "button",
				Text:  "拒否",
				Style: "danger",
			},
		},
	}

	b.postMessageWithAttachment(e, msg, btns)

	b.logger.Printf("%#v\n", invoice)
	return nil
}

func (b *SlackBot) handleBalanceCommand(e *slack.MessageEvent, fromID entity.UserID) error {
	u, err := b.repo.User.FindByID(context.Background(), fromID)
	if err != nil {
		return err
	}

	amount := u.BalanceAmount()
	var msg string
	if amount == entity.Amount(b.cfg.Entity.BalanceLowerLimit) {
		msg = fmt.Sprintf(
			balanceLimitMessage,
			amount,
		)
	} else {
		msg = fmt.Sprintf(
			balanceMessage,
			amount,
			int64(math.Abs(float64(b.cfg.Entity.BalanceLowerLimit-int64(amount)))),
		)
	}
	b.postMessage(e, msg)
	return nil
}

func (b *SlackBot) addDoneReaction(e *slack.MessageEvent) {
	// done as a reaction
	ref := slack.NewRefToMessage(e.Msg.Channel, e.Msg.Timestamp)
	if err := b.client.AddReaction(b.cfg.Controller.Slack.DoneEmoji, ref); err != nil {
		b.logger.Printf("handleMessageEvent: %s", err)
	}
}

func (b *SlackBot) postMessage(e *slack.MessageEvent, msg string) {
	b.client.PostMessage(e.Msg.Channel, msg, slack.PostMessageParameters{
		Username: b.cfg.Controller.Slack.DisplayName,
		AsUser:   true,
	})
}

func (b *SlackBot) postMessageWithAttachment(e *slack.MessageEvent, msg string, attachments ...slack.Attachment) {
	b.client.PostMessage(e.Msg.Channel, msg, slack.PostMessageParameters{
		Username:    b.cfg.Controller.Slack.DisplayName,
		AsUser:      true,
		Attachments: attachments,
	})
}

type parser struct {
	idToSlackUser map[string]slack.User
}

func (p *parser) parse(args []string) (to entity.UserID, amount entity.Amount, err error) {
	n, err := strconv.Atoi(args[0])
	if err == nil {
		// format: 500 @ktr
		amount = entity.Amount(n)
		to, err = p.normalizeUserID(args[1])
		if err != nil {
			return
		}
		return
	}

	n, err = strconv.Atoi(args[1])
	if err == nil {
		// format: @ktr 500
		to, err = p.normalizeUserID(args[1])
		if err != nil {
			return
		}
		amount = entity.Amount(n)
		return
	}

	err = ErrInvalidUsage
	return
}

func (p *parser) normalizeUserID(s string) (entity.UserID, error) {
	res := userIDPattern.FindStringSubmatch(s)
	if len(res) == 2 {
		// if _, ok := p.idToSlackUser[res[1]]; ok {
		return entity.UserID(res[1]), nil
		// }
	}
	return entity.UserID(""), ErrInvalidUserID
}
