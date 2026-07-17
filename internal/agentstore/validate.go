package agentstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/botjoker/sambacrm-business-tg/internal/storage"
)

// validateAndNormalize приводит значение факта к каноничной форме и валидирует
// по field_type. Используется в record_intake_fact ДО записи в БД, чтобы
// отсечь мусор от дешёвых моделей (DeepSeek, gpt-4o-mini часто путают формат).
// При провале возвращает ошибку — вызывающий код возвращает её обратно модели,
// чтобы та переспросила клиента.
//
// Если поле не описано в схеме (fieldInfo == nil) или field_type = string/
// multiline — валидация не выполняется, значение возвращается как есть (trim).
func validateAndNormalize(val string, fieldInfo *storage.AgentIntakeField) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", errors.New("значение пустое")
	}
	// Маска PII от редактора — считается как «клиент дал значение», реальное
	// уже сохранено через IntakeStore.CaptureFromRedaction. Не пишем маску.
	if isPIIMask(val) {
		return "", errors.New("получена маска [PHONE]/[EMAIL] — реальное значение уже захвачено PII-редактором")
	}
	if fieldInfo == nil {
		return val, nil
	}
	switch strings.ToLower(strings.TrimSpace(fieldInfo.FieldType)) {
	case "phone":
		return normalizePhone(val)
	case "email":
		return normalizeEmail(val)
	case "number":
		return normalizeNumber(val)
	case "boolean":
		return normalizeBoolean(val)
	case "date":
		return normalizeDate(val)
	case "enum":
		return validateEnum(val, fieldInfo.FieldOptions)
	default:
		return val, nil
	}
}

// salvageValue — «спасательный» захват, когда strict-нормализация email
// не смогла привести значение к каноничной форме. Задача — не терять данные,
// если клиент написал адрес «по-своему». Телефоны намеренно не спасаем:
// произвольная строка из 7+ цифр неотличима здесь от ИНН или другого номера.
// Возвращает (значение_как_есть, true), если ввод правдоподобно является email;
// иначе (_, false) — тогда вызывающий отклоняет значение.
//
// Сохраняем сырой ввод (trim), а не «почищенный» — чтобы владелец/оператор
// увидел ровно то, что прислал клиент, и мог сверить. Маску PII не спасаем.
func salvageValue(val string, fieldInfo *storage.AgentIntakeField) (string, bool) {
	if fieldInfo == nil {
		return "", false
	}
	val = strings.TrimSpace(val)
	if val == "" || isPIIMask(val) {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(fieldInfo.FieldType)) {
	case "email":
		// Похоже на адрес: есть '@' и точка в доменной части после него.
		if at := strings.IndexByte(val, '@'); at > 0 && strings.IndexByte(val[at:], '.') > 0 {
			return val, true
		}
	}
	return "", false
}

// expectedFormat — человекочитаемая подсказка для промпта переспрашивания.
func expectedFormat(fieldInfo *storage.AgentIntakeField) string {
	if fieldInfo == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(fieldInfo.FieldType)) {
	case "phone":
		return "телефон в формате +7XXXXXXXXXX или 8XXXXXXXXXX"
	case "email":
		return "email вида user@example.com"
	case "number":
		return "число, например 1234 или 1234.56"
	case "boolean":
		return "да / нет"
	case "date":
		return "дата YYYY-MM-DD или DD.MM.YYYY"
	case "enum":
		if opts := parseEnumOptions(fieldInfo.FieldOptions); len(opts) > 0 {
			return "одно из значений: " + strings.Join(opts, ", ")
		}
		return "одно из допустимых значений"
	}
	return ""
}

var piiMasks = map[string]struct{}{
	"[PHONE]": {}, "[EMAIL]": {}, "[CARD]": {},
	"[SNILS]": {}, "[PASSPORT]": {}, "[INN]": {},
}

func isPIIMask(s string) bool {
	_, ok := piiMasks[strings.ToUpper(strings.TrimSpace(s))]
	return ok
}

func normalizePhone(s string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == '+' {
			return r
		}
		return -1
	}, s)
	if cleaned == "" {
		return "", errors.New("нет цифр в номере")
	}
	// Локализованные варианты для РФ: 8XXXXXXXXXX → +7XXXXXXXXXX.
	if strings.HasPrefix(cleaned, "8") && len(cleaned) == 11 {
		cleaned = "+7" + cleaned[1:]
	} else if strings.HasPrefix(cleaned, "7") && len(cleaned) == 11 {
		cleaned = "+" + cleaned
	}
	if !strings.HasPrefix(cleaned, "+") {
		return "", errors.New("номер должен начинаться с +7 или 8")
	}
	digits := strings.TrimPrefix(cleaned, "+")
	if len(digits) < 10 || len(digits) > 15 {
		return "", fmt.Errorf("длина номера %d — вне диапазона 10..15 цифр", len(digits))
	}
	for _, r := range digits {
		if !unicode.IsDigit(r) {
			return "", errors.New("номер содержит нецифровые символы")
		}
	}
	return cleaned, nil
}

func normalizeEmail(s string) (string, error) {
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", fmt.Errorf("некорректный email: %w", err)
	}
	return strings.ToLower(addr.Address), nil
}

func normalizeNumber(s string) (string, error) {
	cleaned := strings.ReplaceAll(s, " ", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	cleaned = strings.ReplaceAll(cleaned, ",", ".")
	if _, err := strconv.ParseFloat(cleaned, 64); err != nil {
		return "", fmt.Errorf("не число: %w", err)
	}
	return cleaned, nil
}

var boolTrue = map[string]struct{}{
	"true": {}, "yes": {}, "y": {}, "1": {}, "on": {},
	"да": {}, "ага": {}, "конечно": {}, "верно": {}, "правда": {},
}
var boolFalse = map[string]struct{}{
	"false": {}, "no": {}, "n": {}, "0": {}, "off": {},
	"нет": {}, "не": {}, "неверно": {}, "ложь": {},
}

func normalizeBoolean(s string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(s))
	if _, ok := boolTrue[k]; ok {
		return "true", nil
	}
	if _, ok := boolFalse[k]; ok {
		return "false", nil
	}
	return "", fmt.Errorf("не булево значение: %q", s)
}

var dateFormats = []string{
	"2006-01-02",
	"02.01.2006",
	"02/01/2006",
	"02-01-2006",
	"2006/01/02",
	"2006.01.02",
	"01/02/2006",
}

func normalizeDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, f := range dateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t.Format("2006-01-02"), nil
		}
	}
	return "", errors.New("дата должна быть YYYY-MM-DD или DD.MM.YYYY")
}

// validateEnum проверяет, что val входит в список опций (case-insensitive).
// options хранится как JSONB — поддерживаем два формата: массив ["a","b"]
// и объект {"options":["a","b"]}.
func validateEnum(val string, optionsJSON []byte) (string, error) {
	opts := parseEnumOptions(optionsJSON)
	if len(opts) == 0 {
		return val, nil // опции не заданы — не с чем сравнивать
	}
	needle := strings.ToLower(strings.TrimSpace(val))
	for _, o := range opts {
		if strings.ToLower(o) == needle {
			return o, nil
		}
	}
	return "", fmt.Errorf("значение %q не в списке: %s", val, strings.Join(opts, ", "))
}

func parseEnumOptions(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	// Попытка 1: плоский массив строк.
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr
	}
	// Попытка 2: массив объектов [{"value":"a","label":"..."}] или {"options":[...]}.
	var wrap struct {
		Options []json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && len(wrap.Options) > 0 {
		return rawMessagesToStrings(wrap.Options)
	}
	var direct []json.RawMessage
	if err := json.Unmarshal(raw, &direct); err == nil && len(direct) > 0 {
		return rawMessagesToStrings(direct)
	}
	return nil
}

func rawMessagesToStrings(items []json.RawMessage) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			out = append(out, s)
			continue
		}
		var obj struct {
			Value string `json:"value"`
			Label string `json:"label"`
		}
		if err := json.Unmarshal(item, &obj); err == nil {
			if obj.Value != "" {
				out = append(out, obj.Value)
			} else if obj.Label != "" {
				out = append(out, obj.Label)
			}
		}
	}
	return out
}

// valueToString приводит args["value"] от LLM к строке. LLM может передать
// строку, число, bool или объект — нормализуем всё в строку для валидации.
func valueToString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// json.Unmarshal числа в float64. Целое выводим без .0.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	// Объекты/массивы — сериализуем в JSON.
	if raw, err := json.Marshal(v); err == nil {
		return string(raw)
	}
	return fmt.Sprint(v)
}
