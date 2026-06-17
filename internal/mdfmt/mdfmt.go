// Package mdfmt — конвертация Markdown (как его генерит LLM) в форматы каналов:
// ToTelegramHTML — для Telegram (parse_mode=HTML), ToPlain — для VK и прочего,
// где разметка не поддерживается. Покрывает частое подмножество CommonMark
// (жирный, курсив, inline-code, ссылки, заголовки, маркеры списков).
package mdfmt

import (
	"html"
	"regexp"
)

var (
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reCode     = regexp.MustCompile("`([^`]+)`")
	reBoldStar = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reBoldUnd  = regexp.MustCompile(`__([^_]+)__`)
	reItalStar = regexp.MustCompile(`\*([^*\n]+)\*`)
	reItalUnd  = regexp.MustCompile(`(?:^|[\s(])_([^_\n]+)_`) // _x_ только как отдельное слово
	reHeading  = regexp.MustCompile(`(?m)^\s*#{1,6}\s+(.*)$`)
	reBullet   = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
)

// ToTelegramHTML переводит Markdown в безопасный для Telegram HTML.
// Сначала экранирует <>&, поэтому пользовательский текст не сломает разметку.
func ToTelegramHTML(md string) string {
	s := html.EscapeString(md)
	s = reLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = reCode.ReplaceAllString(s, `<code>$1</code>`)
	s = reBoldStar.ReplaceAllString(s, `<b>$1</b>`)
	s = reBoldUnd.ReplaceAllString(s, `<b>$1</b>`)
	s = reHeading.ReplaceAllString(s, `<b>$1</b>`)
	s = reBullet.ReplaceAllString(s, "$1• ")
	s = reItalStar.ReplaceAllString(s, `<i>$1</i>`)
	return s
}

// ToPlain убирает Markdown-разметку, оставляя читаемый текст (для VK и т.п.).
func ToPlain(md string) string {
	s := reLink.ReplaceAllString(md, "$1 ($2)")
	s = reCode.ReplaceAllString(s, "$1")
	s = reBoldStar.ReplaceAllString(s, "$1")
	s = reBoldUnd.ReplaceAllString(s, "$1")
	s = reHeading.ReplaceAllString(s, "$1")
	s = reBullet.ReplaceAllString(s, "$1• ")
	s = reItalStar.ReplaceAllString(s, "$1")
	return s
}
