package handlers

import (
	"encoding/json"
	"errors"

	"github.com/hyperlab-sh/husk/internal/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// Reply encodes result as req's successful reply. A nil result encodes {},
// so notifications reply with the empty object like every other void method.
// The id is echoed when present; the Server's connection rewrites it onto its
// own request table anyway.
func Reply(req protocol.Message, result any) protocol.Message {
	if result == nil {
		result = protocol.EmptyResult{}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return *errReply(req, protocol.CodeInternal, "encoding reply: "+err.Error())
	}
	m := protocol.Message{Result: raw}
	if req.ID != nil {
		m.ID = req.ID
	}
	return m
}

// ErrorReply maps a port error to the wire reply. A contracts.CodedError
// keeps its code — plus its data when it also implements DataError — and any
// other error becomes internal with the error's own message, so adapters map
// by code, never by message text.
func ErrorReply(req protocol.Message, err error) protocol.Message {
	if err == nil {
		return Reply(req, nil)
	}
	e := &protocol.Error{Message: err.Error()}
	var coded contracts.CodedError
	if errors.As(err, &coded) {
		e.Code = protocol.Code(coded.Code())
		if !e.Code.Valid() {
			e.Code = protocol.CodeInternal
		}
		var withData contracts.DataError
		if errors.As(err, &withData) && len(withData.Data()) > 0 {
			if raw, merr := json.Marshal(withData.Data()); merr == nil {
				e.Data = raw
			}
		}
	} else {
		e.Code = protocol.CodeInternal
	}
	m := protocol.Message{Error: e}
	if req.ID != nil {
		m.ID = req.ID
	}
	return m
}

// UnknownMethod is the reply for a method no adapter serves.
func UnknownMethod(req protocol.Message) protocol.Message {
	return *errReply(req, protocol.CodeUnknownMethod, "no inbound handler for "+req.Method)
}

// InvalidParams is the reply for malformed parameters, naming the offending
// field ("params.slice", or "params" when the whole object has the wrong
// shape). detail says what failed.
func InvalidParams(req protocol.Message, field, detail string) protocol.Message {
	data, _ := json.Marshal(map[string]string{"field": field})
	m := protocol.Message{Error: &protocol.Error{
		Code:    protocol.CodeInvalidParams,
		Message: field + ": " + detail,
		Data:    data,
	}}
	if req.ID != nil {
		m.ID = req.ID
	}
	return m
}

// DecodeParams unmarshals req.Params into dst. Absent params leave dst at its
// zero value, which per-method required-field checks then reject. A decode
// failure returns the invalid_params reply naming the offending field —
// "params.<field>" for a typed field, "params" when the whole object has the
// wrong shape. Adapters call it before their port, never after.
func DecodeParams(req protocol.Message, dst any) *protocol.Message {
	if len(req.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		field := "params"
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			field = "params." + te.Field
		}
		bad := InvalidParams(req, field, err.Error())
		return &bad
	}
	return nil
}

// RequireString rejects an empty required string field. Adapters pair it
// with DecodeParams: decode first, then reject blank identifiers while
// allowing values that may legitimately be empty strings.
func RequireString(req protocol.Message, field, value string) *protocol.Message {
	if value == "" {
		bad := InvalidParams(req, field, "is required")
		return &bad
	}
	return nil
}

// Denied is the plain permission-refusal reply: code denied with message and
// the request id echoed. Admit and the adapters' own denial rows build on
// it; never hand-assemble the envelope.
func Denied(req protocol.Message, message string) protocol.Message {
	return *errReply(req, protocol.CodeDenied, message)
}
