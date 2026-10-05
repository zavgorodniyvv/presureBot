package logging_test

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/zavgorodniyvv/presureBot/internal/logging"
)

const fakeToken = "123456789:AAF7dummyTokenValueForLeakTest_0123456789"

// Порт 1 закрыт — Client.Do вернёт *url.Error с полным URL внутри.
const deadEndpoint = "http://127.0.0.1:1/bot%s/%s"

func TestTokenLeaksWithoutRedaction(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	_, err := tgbotapi.NewBotAPIWithAPIEndpoint(fakeToken, deadEndpoint)
	log.Println(err)

	if !strings.Contains(buf.String(), fakeToken) {
		t.Fatalf("ожидали утечку токена в сыром логе, получили: %s", buf.String())
	}
	t.Logf("СЫРОЙ ЛОГ: %s", buf.String())
}

func TestTokenRedacted(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(logging.NewRedactingWriter(&buf, fakeToken))
	defer log.SetOutput(os.Stderr)

	_, err := tgbotapi.NewBotAPIWithAPIEndpoint(fakeToken, deadEndpoint)
	log.Println(err)

	if strings.Contains(buf.String(), fakeToken) {
		t.Fatalf("токен утёк: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "REDACTED") {
		t.Fatalf("маска не проставлена: %s", buf.String())
	}
	t.Logf("ОТФИЛЬТРОВАННЫЙ ЛОГ: %s", buf.String())
}
