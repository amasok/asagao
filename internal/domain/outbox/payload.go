package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// The payload of an entry is stored as a JSON object (the outbox payload column), and Op
// keeps it as it was read (Op.Payload). The code that sends, undoes or describes an entry
// reads it as its kind's struct (payloadTypes) through PayloadOf, and builds one with
// EncodePayload: this file is the only place that converts between the two.
//
// The stored form does not change: each struct reads every key the entries of its kind
// have been written with, and writes them back the same (TestPayloadRoundTrip). A key it
// does not know or a value of another type (written by a newer version, or damaged) is an
// error, *PayloadError: such an entry is held, never sent with a part of it left out.

// ErrUnknownKind is the error for an entry of a kind this version does not know (written
// by a newer one).
var ErrUnknownKind = errors.New("この版では扱えない種類の変更です")

// PayloadError says that the payload of an entry cannot be read as its kind's struct.
type PayloadError struct {
	Kind Kind
	Err  error
}

func (e *PayloadError) Error() string {
	return fmt.Sprintf("%s の内容を読めません: %v", e.Kind, e.Err)
}

func (e *PayloadError) Unwrap() error { return e.Err }

// payloadTypes holds the payload struct of every kind (TestEveryKindHasAPayload). Kinds
// whose entries hold the same things share one.
var payloadTypes = map[Kind]reflect.Type{}

func payloadType[P any](kinds ...Kind) {
	for _, k := range kinds {
		payloadTypes[k] = reflect.TypeFor[P]()
	}
}

// PayloadType returns the payload struct of kind k (ok is false for an unknown kind).
func PayloadType(k Kind) (t reflect.Type, ok bool) {
	t, ok = payloadTypes[k]
	return t, ok
}

// CheckPayloadType panics unless P is the payload struct of kind k. The registries of the
// sync engine and the store call it when a handler is registered for k with P.
func CheckPayloadType[P any](k Kind) {
	if t, ok := payloadTypes[k]; !ok || t != reflect.TypeFor[P]() {
		panic(fmt.Sprintf("outbox: %v is not the payload of kind %q (%v)", reflect.TypeFor[P](), k, t))
	}
}

// PayloadOf returns the payload of op as P, the struct of its kind. The error is
// ErrUnknownKind for a kind this version does not know, or a *PayloadError when the
// payload cannot be read; P not being op's kind's struct is a programming error (an
// error too, not a panic, so that one entry cannot stop a push).
func PayloadOf[P any](op Op) (P, error) {
	var p P
	t, ok := payloadTypes[op.Kind]
	switch {
	case !ok:
		return p, fmt.Errorf("%q: %w", op.Kind, ErrUnknownKind)
	case t != reflect.TypeFor[P]():
		return p, fmt.Errorf("outbox: %v is not the payload of kind %q (%v)", reflect.TypeFor[P](), op.Kind, t)
	}
	if err := decodePayload(op.Payload, &p); err != nil {
		return p, &PayloadError{Kind: op.Kind, Err: err}
	}
	return p, nil
}

// BestEffortPayload returns the payload of op as P as far as it reads, for what is still
// done with an entry whatever this version can send of it (undoing its local change when
// it is discarded, saying what it is): a key P does not know is left out, a value of
// another type leaves its field zero. err is PayloadOf's (nil: it read entirely); P not
// being op's kind's struct gives a zero P.
func BestEffortPayload[P any](op Op) (P, error) {
	p, err := PayloadOf[P](op)
	var pe *PayloadError
	if !errors.As(err, &pe) {
		return p, err
	}
	var q P
	if b, merr := json.Marshal(op.Payload); merr == nil {
		_ = json.Unmarshal(b, &q) // fills in every field that reads
	}
	return q, err
}

// DecodePayload returns the payload of op as its kind's struct (a value of PayloadType),
// with the errors of PayloadOf.
func DecodePayload(op Op) (any, error) {
	t, ok := payloadTypes[op.Kind]
	if !ok {
		return nil, fmt.Errorf("%q: %w", op.Kind, ErrUnknownKind)
	}
	p := reflect.New(t)
	if err := decodePayload(op.Payload, p.Interface()); err != nil {
		return nil, &PayloadError{Kind: op.Kind, Err: err}
	}
	return p.Elem().Interface(), nil
}

// EncodePayload returns payload p in its stored form, for an entry of kind k (p must be
// k's struct).
func EncodePayload(k Kind, p any) (map[string]any, error) {
	t, ok := payloadTypes[k]
	switch {
	case !ok:
		return nil, fmt.Errorf("%q: %w", k, ErrUnknownKind)
	case reflect.TypeOf(p) != t:
		return nil, fmt.Errorf("outbox: %T is not the payload of kind %q (%v)", p, k, t)
	}
	b, err := json.Marshal(p)
	if err != nil { // e.g. a NaN custom field value
		return nil, &PayloadError{Kind: k, Err: err}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, &PayloadError{Kind: k, Err: err}
	}
	return m, nil
}

// decodePayload reads the stored form m into the struct dst points to: every key must be
// one of its fields, with a value of the field's type.
func decodePayload(m map[string]any, dst any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// Readable reports why the payload of op cannot be read as its kind's struct (nil when it
// can): ErrUnknownKind, or a *PayloadError.
func Readable(op Op) error {
	_, err := DecodePayload(op)
	return err
}

// Fields returns the fields the update sets, in the stored form (key → value).
func (p UpdateTask) Fields() map[string]any { return fieldsOf(p) }

// Fields returns the fields of the create in the stored form (key → value), with where
// the task goes (workspace, parent, project, section) and custom_fields.
func (p CreateTask) Fields() map[string]any { return fieldsOf(p) }

// Fields returns the fields the update sets, in the stored form (key → value).
func (p UpdateProject) Fields() map[string]any { return fieldsOf(p) }

// fieldsOf returns the struct v as the stored form reads back (a JSON object decoded into
// a map). The payload structs always encode.
func fieldsOf(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("outbox: %T does not encode: %v", v, err))
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic(fmt.Sprintf("outbox: %T does not decode: %v", v, err))
	}
	return m
}
