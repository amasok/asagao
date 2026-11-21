package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

// The payload of an entry is stored as a JSON object (the outbox payload column), and Op
// keeps it as it was read (Op.Payload). The code that sends, undoes or describes an entry
// reads it as its kind's struct (payloadTypes) through PayloadOf, and the code that queues
// or changes one writes it from the struct with SetPayload, EditPayload or MergeFields
// (over EncodePayload): this file is the only place that converts between the two
// (archtest: TestOutboxPayloadsAreBuiltFromTheirStructs).
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
	readAsFarAsItReads(op.Payload, &q)
	return q, err
}

// readAsFarAsItReads reads the stored form m into the struct dst points to key by key: a
// key that does not read as one of its fields (unknown, or a value of another type) is
// left out, and its field stays zero (a pointer nil, not pointing to a zero value).
func readAsFarAsItReads(m map[string]any, dst any) {
	t := reflect.TypeOf(dst).Elem()
	for _, k := range slices.Sorted(maps.Keys(m)) {
		one := map[string]any{k: m[k]}
		if decodePayload(one, reflect.New(t).Interface()) != nil {
			continue
		}
		_ = decodePayload(one, dst)
	}
}

// ReadsField reports whether the stored form of op has key, with a value (not null) that
// reads as the field of its kind's struct: what a field that is always written (a bool, a
// string) reads as when it is absent or of another type cannot tell.
func ReadsField(op Op, key string) bool {
	t, ok := payloadTypes[op.Kind]
	v, has := op.Payload[key]
	if !ok || !has || v == nil {
		return false
	}
	return decodePayload(map[string]any{key: v}, reflect.New(t).Interface()) == nil
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

// SetPayload sets the payload of op to p, its kind's struct, in the stored form
// (EncodePayload). It replaces the whole payload: an entry joined by a change that keeps
// some of what it holds uses EditPayload.
func SetPayload[P any](op *Op, p P) error {
	m, err := EncodePayload(op.Kind, p)
	if err != nil {
		return err
	}
	op.Payload = m
	return nil
}

// EditPayload changes the payload of op through its kind's struct P: f gets it as it reads
// (BestEffortPayload), and only the keys whose value f changes are written back (a field
// f leaves out of the stored form, a pointer set to nil or a zero value it omits, is
// taken out). Every other key stays as stored, one P does not know among them, so an
// entry this version cannot read (written by a newer one) takes the change all the same:
// it is held with it, and the version that reads it sends them together.
func EditPayload[P any](op *Op, f func(*P)) error {
	if t, ok := payloadTypes[op.Kind]; !ok {
		return fmt.Errorf("%q: %w", op.Kind, ErrUnknownKind)
	} else if t != reflect.TypeFor[P]() {
		return fmt.Errorf("outbox: %v is not the payload of kind %q (%v)", reflect.TypeFor[P](), op.Kind, t)
	}
	// Read as a whole, not key by key (readAsFarAsItReads): a pointer field holding a value
	// of another type then points to a zero value, so that f clearing it (nil) takes the
	// key out, while one f leaves as it is stays as stored.
	var p P
	if b, err := json.Marshal(op.Payload); err == nil {
		_ = json.Unmarshal(b, &p) // fills in every field that reads
	}
	before, err := EncodePayload(op.Kind, p)
	if err != nil {
		return err
	}
	f(&p)
	after, err := EncodePayload(op.Kind, p)
	if err != nil {
		return err
	}
	out := maps.Clone(op.Payload)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range after {
		_, stored := out[k] // (a key of the stored form a new payload does not have yet)
		if old, ok := before[k]; !ok || !stored || !reflect.DeepEqual(old, v) {
			out[k] = v
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			delete(out, k)
		}
	}
	op.Payload = out
	return nil
}

// CheckFields reports whether fields, the changes of an entry of kind k merged into its
// payload key by key, read as k's struct (nil when they do): the error is EncodePayload's
// for an unknown kind, or a *PayloadError for a key it does not have or a value of
// another type.
func CheckFields(k Kind, fields map[string]any) error {
	t, ok := payloadTypes[k]
	if !ok {
		return fmt.Errorf("%q: %w", k, ErrUnknownKind)
	}
	if err := decodePayload(fields, reflect.New(t).Interface()); err != nil {
		return &PayloadError{Kind: k, Err: err}
	}
	return nil
}

// PayloadFields returns the payload of op as its kind's struct reads it, in the stored form
// (key → value), for what shows it key by key (the fields of a conflict): as
// BestEffortPayload, a key the struct does not know is left out, and a value of another
// type reads as its field's zero value. It is nil for a kind this version does not know.
func PayloadFields(op Op) map[string]any {
	t, ok := payloadTypes[op.Kind]
	if !ok {
		return nil
	}
	p := reflect.New(t)
	readAsFarAsItReads(op.Payload, p.Interface())
	return fieldsOf(p.Elem().Interface())
}

// MergeFields merges fields, the changes of a task (an edit's fields, of UpdateTask), into
// the payload of op key by key: a create not sent yet is sent with them (see CreateTask).
// They must read as the payload struct of op's kind (CheckFields); the keys of the payload
// they do not change stay as they are, so an entry this version cannot read takes them
// all the same (held with them).
func MergeFields(op *Op, fields map[string]any) error {
	if err := CheckFields(op.Kind, fields); err != nil {
		return err
	}
	if op.Payload == nil {
		op.Payload = map[string]any{}
	}
	maps.Copy(op.Payload, fields)
	return nil
}

// PayloadName returns the name the payload of op holds (the task, section, tag, file …
// its kind's struct calls name), as far as it reads: "" when it has none. For a kind this
// version does not know, it is the stored form's "name" if that is a string (what such an
// entry most likely names, shown when it is discarded).
func PayloadName(op Op) string {
	fields := PayloadFields(op)
	if fields == nil {
		fields = op.Payload
	}
	name, _ := fields["name"].(string)
	return name
}
