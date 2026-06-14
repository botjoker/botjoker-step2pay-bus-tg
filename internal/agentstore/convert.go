// Package agentstore — sqlc-backed адаптеры рантайм-портов (Memory, Recorder,
// Intake, Takeover, Billing). Зависит от сгенерированного internal/storage,
// поэтому компилируется только ПОСЛЕ `make sqlc`. Ядро internal/runtime от
// этого пакета не зависит — он подключается в cmd/agent при сборке сервиса.
package agentstore

import (
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func toUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func fromUUID(p pgtype.UUID) uuid.UUID {
	if !p.Valid {
		return uuid.Nil
	}
	return uuid.UUID(p.Bytes)
}

// toUUIDOrNull → NULL, если id == uuid.Nil (для nullable FK).
func toUUIDOrNull(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

func toText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func fromText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func toInt4(n int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(n), Valid: true}
}

func toBool(b bool) pgtype.Bool {
	return pgtype.Bool{Bool: b, Valid: true}
}

// toNumeric конвертирует float64 в pgtype.Numeric через строковый Scan.
// При ошибке возвращает невалидный (NULL) numeric.
func toNumeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(f, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{}
	}
	return n
}
