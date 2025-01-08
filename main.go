package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/shamaton/msgpack/v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

var state State

type DiskSyncStore struct{}
type DiskSync struct {
	Filters   map[id.UserID]string
	NextBatch map[id.UserID]string
}

type Settings struct {
	Homeserver  string
	UserID      string
	AccessToken string
	Token       string
	Channels    map[string]string
}

type State struct {
	M *mautrix.Client
	D *discordgo.Session
	S *Settings
}

func (d *DiskSyncStore) SaveFilterID(ctx context.Context, userID id.UserID, filterID string) error {
	s := &DiskSync{}

	f, err := os.OpenFile("data/syncstore", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	err = msgpack.UnmarshalRead(f, s)
	if err != nil {
		return err
	}

	s.Filters[userID] = filterID
	err = msgpack.MarshalWrite(f, s)
	if err != nil {
		return err
	}

	return nil
}

func (d *DiskSyncStore) LoadFilterID(ctx context.Context, userID id.UserID) (string, error) {
	s := &DiskSync{}

	f, err := os.Open("data/syncstore")
	if err != nil {
		return "", err
	}
	defer f.Close()

	err = msgpack.UnmarshalRead(f, s)
	if err != nil {
		return "", err
	}

	return s.Filters[userID], nil
}

func (d *DiskSyncStore) SaveNextBatch(ctx context.Context, userID id.UserID, nextBatchToken string) error {
	s := &DiskSync{}

	f, err := os.OpenFile("data/syncstore", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	err = msgpack.UnmarshalRead(f, s)
	if err != nil {
		return err
	}

	s.NextBatch[userID] = nextBatchToken
	err = msgpack.MarshalWrite(f, s)
	if err != nil {
		return err
	}

	return nil
}

func (d *DiskSyncStore) LoadNextBatch(ctx context.Context, userID id.UserID) (string, error) {
	s := &DiskSync{}

	f, err := os.Open("data/syncstore")
	if err != nil {
		return "", err
	}
	defer f.Close()

	err = msgpack.UnmarshalRead(f, s)
	if err != nil {
		return "", err
	}

	return s.NextBatch[userID], nil
}

func keyByValue(m map[string]string, val string) string {
	for k, v := range m {
		if v == val {
			return k
		}
	}
	return ""
}

func renderDiscord(e *event.Event) string {
	body := ""

	url := e.Content.AsMessage().URL.ParseOrIgnore().String()
	if url != "" {
		body += "`" + e.Sender.String() + "` " + url
	}

	body += "`" + e.Sender.String() + "` " + e.Content.AsMessage().Body

	return body
}

func renderMatrix(e *discordgo.MessageCreate) event.MessageEventContent {
	body := "`" + e.Author.Username + "` " + e.Content
	for _, attachment := range e.Attachments {
		body += "\n`" + e.Author.Username + "` " + attachment.URL
	}

	return format.RenderMarkdown(body, true, false)
}

func matrixInvite(ctx context.Context, e *event.Event) {
	if e.Content.AsMember().Membership != event.MembershipInvite {
		return
	}

	_, err := state.M.JoinRoomByID(ctx, e.RoomID)
	if err != nil {
		log.Err(err).Msg("")
		return
	}
}

func matrixMessage(ctx context.Context, e *event.Event) {
	if e.Sender.String() == state.S.UserID {
		return
	}
	channel := keyByValue(state.S.Channels, e.RoomID.String())

	body := renderDiscord(e)
	_, err := state.D.ChannelMessageSend(
		channel,
		body,
	)
	if err != nil {
		log.Err(err).Msg("")
		return
	}
}

func discordMessage(s *discordgo.Session, e *discordgo.MessageCreate) {
	if e.Author.ID == state.D.State.User.ID {
		return
	}

	room := state.S.Channels[e.ChannelID]

	content := renderMatrix(e)
	_, err := state.M.SendMessageEvent(
		context.TODO(),
		id.RoomID(room),
		event.EventMessage,
		&content,
	)
	if err != nil {
		log.Err(err).Msg("")
		return
	}
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).With().Caller().Logger()

	settings_file, err := os.Open("settings.json")

	if errors.Is(err, os.ErrNotExist) {
		template := &Settings{
			Homeserver:  "homeserver.example",
			UserID:      "example",
			AccessToken: "example1111",
			Token:       "discordtokenexample",
			Channels: map[string]string{
				"discord": "matrix",
			},
		}

		f, err := os.Create("settings.json")
		if err != nil {
			log.Err(err).Msg("")
			return
		}
		defer f.Close()

		encoder := json.NewEncoder(f)
		encoder.SetIndent("", "    ")
		err = encoder.Encode(template)
		if err != nil {
			log.Err(err).Msg("")
			return
		}

		log.Info().Msg("Made template settings file at settings.json. Modify settings")
		return
	}

	if err != nil {
		log.Err(err).Msg("")
		return
	}
	defer settings_file.Close()

	settings := &Settings{}
	err = json.NewDecoder(settings_file).Decode(settings)
	if err != nil {
		log.Err(err).Msg("")
		return
	}

	if _, err := os.Stat("data"); errors.Is(err, os.ErrNotExist) {
		err = os.Mkdir("data", 0)
		if err != nil {
			log.Err(err).Msg("")
			return
		}

		log.Info().Msg("Made data directory")
	}

	if _, err := os.Stat("data/syncstore"); errors.Is(err, os.ErrNotExist) {
		f, err := os.Create("data/syncstore")
		if err != nil {
			log.Err(err).Msg("")
			return
		}
		defer f.Close()

		store := &DiskSync{
			Filters:   make(map[id.UserID]string),
			NextBatch: make(map[id.UserID]string),
		}
		err = msgpack.MarshalWrite(f, store)
		if err != nil {
			log.Err(err).Msg("")
			return
		}

		log.Info().Msg("Made syncstore file")
	}

	matrix, err := mautrix.NewClient(
		settings.Homeserver,
		id.UserID(settings.UserID),
		settings.AccessToken,
	)
	if err != nil {
		log.Err(err).Msg("")
		return
	}

	matrix.Store = &DiskSyncStore{}
	syncer := matrix.Syncer.(mautrix.ExtensibleSyncer)
	syncer.OnSync(matrix.DontProcessOldEvents)

	syncer.OnEventType(event.EventMessage, matrixMessage)
	syncer.OnEventType(event.StateMember, matrixInvite)

	discord, err := discordgo.New(settings.Token)
	if err != nil {
		log.Err(err).Msg("")
		return
	}

	discord.Identify.Intents =
		discordgo.IntentsAllWithoutPrivileged | discordgo.IntentMessageContent

	discord.AddHandler(discordMessage)

	state = State{
		M: matrix,
		D: discord,
		S: settings,
	}

	err = discord.Open()
	if err != nil {
		log.Err(err).Msg("")
		return
	}

	go func() {
		err = matrix.Sync()
		if err != nil {
			log.Err(err).Msg("")
			return
		}
	}()

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, os.Interrupt)
	<-sigch

	err = discord.Close()
	if err != nil {
		log.Err(err).Msg("")
		return
	}
}
