// Package logging содержит обёртки над стандартным логгером.
package logging

import (
	"io"
	"net/url"
	"strings"
)

const mask = "***REDACTED***"

// minSecretLen отсекает слишком короткие значения: маскировать их опасно,
// они могут случайно совпасть с обычным текстом в логах.
const minSecretLen = 8

// redactingWriter заменяет секреты в логах на маску перед записью.
// Нужен потому, что телеграм-токен попадает в путь URL, а сетевые ошибки
// (*url.Error) печатают URL целиком — в том числе внутри библиотеки tgbotapi.
type redactingWriter struct {
	w        io.Writer
	replacer *strings.Replacer
}

func (rw *redactingWriter) Write(p []byte) (int, error) {
	if _, err := rw.w.Write([]byte(rw.replacer.Replace(string(p)))); err != nil {
		return 0, err
	}
	// Возвращаем длину исходных данных: log считает короткую запись ошибкой.
	return len(p), nil
}

// NewRedactingWriter оборачивает w так, чтобы все вхождения secrets заменялись
// на маску. Пустые и слишком короткие секреты игнорируются.
func NewRedactingWriter(w io.Writer, secrets ...string) io.Writer {
	pairs := make([]string, 0, len(secrets)*2)
	for _, s := range secrets {
		if len(s) < minSecretLen {
			continue
		}
		pairs = append(pairs, s, mask)
	}

	if len(pairs) == 0 {
		return w
	}

	return &redactingWriter{w: w, replacer: strings.NewReplacer(pairs...)}
}

// SecretsFromURI возвращает секретные части строки подключения: сам URI
// целиком и пароль отдельно — драйвер может логировать их по частям.
func SecretsFromURI(uri string) []string {
	if uri == "" {
		return nil
	}

	secrets := []string{uri}

	u, err := url.Parse(uri)
	if err != nil || u.User == nil {
		return secrets
	}

	if password, ok := u.User.Password(); ok {
		secrets = append(secrets, password)
	}

	return secrets
}
