package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // в alpine-образе нет базы часовых поясов

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/bot"
	"github.com/zavgorodniyvv/presureBot/internal/logging"
	"github.com/zavgorodniyvv/presureBot/internal/recognize"
	"github.com/zavgorodniyvv/presureBot/internal/storage"
)

func main() {
	token := os.Getenv("TELEGRAM_TOKEN")
	mongoURI := os.Getenv("MONGODB_URI")
	openAIKey := os.Getenv("OPENAI_API_KEY")

	// Ставим фильтр до первого лога: сетевые ошибки печатают URL Telegram API
	// вместе с токеном, в том числе из горутины GetUpdatesChan.
	secrets := append([]string{token, openAIKey}, logging.SecretsFromURI(mongoURI)...)
	log.SetOutput(logging.NewRedactingWriter(os.Stderr, secrets...))

	for name, v := range map[string]string{"TELEGRAM_TOKEN": token, "MONGODB_URI": mongoURI, "OPENAI_API_KEY": openAIKey} {
		if v == "" {
			log.Fatalf("%s is empty", name)
		}
	}

	allowed, err := parseIDs(os.Getenv("ALLOWED_USER_IDS"))
	if err != nil {
		log.Fatalf("ALLOWED_USER_IDS: укажите Telegram ID через запятую: %v", err)
	}
	if len(allowed) == 0 {
		// Запускаемся без доступа для всех: так бот может сообщить пользователю его ID.
		log.Print("ВНИМАНИЕ: ALLOWED_USER_IDS пуст — бот никого не обслуживает, только сообщает Telegram ID")
	}

	loc, err := time.LoadLocation(env("TZ", "Asia/Jerusalem"))
	if err != nil {
		log.Fatalf("TZ: %v", err)
	}
	halfLife, err := time.ParseDuration(env("TREND_HALF_LIFE", "72h"))
	if err != nil {
		log.Fatalf("TREND_HALF_LIFE: %v", err)
	}
	sessionTimeout, err := time.ParseDuration(env("SESSION_TIMEOUT", "20m"))
	if err != nil {
		log.Fatalf("SESSION_TIMEOUT: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := storage.NewMongo(ctx, mongoURI, env("MONGODB_DATABASE", "pressurebot"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("запущен как @%s", api.Self.UserName)

	b := bot.New(api, recognize.NewOpenAI(openAIKey, env("OPENAI_MODEL", "gpt-4.1")), st, bot.Config{
		AllowedUsers:   allowed,
		Location:       loc,
		TrendHalfLife:  halfLife,
		SessionTimeout: sessionTimeout,
	})

	upd := tgbotapi.NewUpdate(0)
	upd.Timeout = 60
	updates := api.GetUpdatesChan(upd)

	for {
		select {
		case <-ctx.Done():
			log.Print("остановка")
			api.StopReceivingUpdates()
			return
		case u := <-updates:
			// Распознавание фото занимает секунды — не блокируем остальные сообщения.
			go b.Handle(u)
		}
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func parseIDs(s string) (map[int64]bool, error) {
	ids := make(map[int64]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, nil
}
