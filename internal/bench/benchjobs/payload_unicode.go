package benchjobs

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/cidekar/adele-queue/api"
)

type UnicodePayload struct {
	Text  string `json:"text"`
	Emoji string `json:"emoji"`
	Lang  string `json:"lang"`
}

func UnicodeHandler(payload interface{}) error {
	raw, ok := payload.([]byte)
	if !ok {
		return fmt.Errorf("unicode: payload not []byte, got %T", payload)
	}
	var p UnicodePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("unicode: unmarshal: %w", err)
	}
	fmt.Printf("unicode.text  : %q bytes=%d runes=%d\n", p.Text, len(p.Text), utf8.RuneCountInString(p.Text))
	fmt.Printf("unicode.emoji : %q bytes=%d runes=%d\n", p.Emoji, len(p.Emoji), utf8.RuneCountInString(p.Emoji))
	fmt.Printf("unicode.lang  : %q bytes=%d runes=%d\n", p.Lang, len(p.Lang), utf8.RuneCountInString(p.Lang))
	return nil
}

func NewUnicodeJob() (*api.Job, error) {
	p := UnicodePayload{
		// Arabic (RTL), Chinese, Cyrillic, combining char, zero-width joiner.
		Text:  "hello مرحبا 你好 Здравствуйте café\u200d",
		Emoji: "👨\u200d👩\u200d👧 🏳️\u200d🌈 🇺🇸",
		Lang:  "ASCII-1byte/rune; 漢=3bytes/1rune; 🇺🇸=8bytes/2runes",
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return &api.Job{
		Name:    "payload-unicode",
		Payload: raw,
		Handler: UnicodeHandler,
		Queue:   "job",
		Retry:   false,
	}, nil
}
