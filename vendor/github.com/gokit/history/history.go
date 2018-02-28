package history

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// level constants
const (
	RedLvl Level = iota
	YellowLvl
	ErrorLvl
	InfoLvl
)

// errors ...
var (
	ErrYellowAlert = errors.New("warning: error or invalid state occured")
	ErrRedAlert    = errors.New("very bad: earth damaging error occured, check now")
)

var (
	hl = struct {
		ml  sync.Mutex
		hls []Handler
	}{}
)

// WithHandlers  returns a new Source with giving Handlers as receivers of
// all provided instances of B.
func WithHandlers(hs ...Handler) Ctx {
	var handler Handlers
	handler = append(handler, hs...)

	hl.ml.Lock()
	handler = append(handler, hl.hls...)
	hl.ml.Unlock()

	return &BugLog{bugs: handler, Signature: randName(20), Title: "BugLog", From: time.Now()}
}

// FromTags returns a new Source with giving tags as default tags
// added to all B instances submitted through Source's Ctx instances.
func FromTag(hs ...string) Ctx {
	var handler Handlers
	hl.ml.Lock()
	handler = append(handler, hl.hls...)
	hl.ml.Unlock()

	return &BugLog{bugs: handler, Tags: hs, Signature: randName(20), Title: "BugLog", From: time.Now()}
}

// FromFields returns a new Source with giving fields as default tags
// added to all B instances submitted through Source's Ctx instances.
func FromFields(hs map[string]interface{}) Ctx {
	var handler Handlers
	hl.ml.Lock()
	handler = append(handler, hl.hls...)
	hl.ml.Unlock()

	bug := &BugLog{bugs: handler, Signature: randName(20), Title: "BugLog", From: time.Now()}
	return bug.WithFields(hs)
}

// SetDefaultHandlers sets default Handlers to be included
// in all instances of Sources to be used to process provided
// BugLogs.
func SetDefaultHandlers(hs ...Handler) {
	hl.ml.Lock()
	defer hl.ml.Unlock()
	hl.hls = append(hl.hls, hs...)
}

//**************************************************************
// Handler Interface
//**************************************************************

// Recv defines a function type which receives a pointer of B.
type Recv func(*BugLog) error

// Handler exposes a single method to deliver giving B value to
type Handler interface {
	Recv(*BugLog) error
}

// HandlerFunc returns a new Handler using provided function for
// calls to the Handler.Recv method.
func HandlerFunc(recv Recv) Handler {
	return fnHandler{rc: recv}
}

//Handlers defines a slice of Handlers as a type.
type Handlers []Handler

// Recv calls individual Handler.Recv in slice with BugLog instance.
func (h Handlers) Recv(b *BugLog) error {
	for _, hl := range h {
		if err := hl.Recv(b); err != nil {
			return err
		}
	}
	return nil
}

type fnHandler struct {
	rc Recv
}

// Recv implements the Handler and calls the underline Recv
// function provided with provided B pointer.
func (fn fnHandler) Recv(b *BugLog) error {
	return fn.rc(b)
}

//**************************************************************
// Level
//**************************************************************

// Level defines a int type which represent the a giving level of entry for a giving entry.
type Level int

// GetLevel returns Level value for the giving string.
// It returns -1 if it does not know the level string.
func GetLevel(lvl string) Level {
	switch strings.ToLower(lvl) {
	case "red":
		return RedLvl
	case "yellow":
		return YellowLvl
	case "error":
		return ErrorLvl
	case "info":
		return InfoLvl
	}

	return -1
}

// String returns the string version of the Level.
func (l Level) String() string {
	switch l {
	case RedLvl:
		return "red"
	case YellowLvl:
		return "yellow"
	case ErrorLvl:
		return "error"
	case InfoLvl:
		return "info"
	}

	return "UNKNOWN"
}

//**************************************************************
// Location
//**************************************************************

// Location defines the location which an history occured in.
type Location struct {
	Function string `json:"function"`
	Line     int    `json:"line"`
	File     string `json:"file"`
}

//**************************************************************
// Field
//**************************************************************

// Field represents a giving key-value pair with location details.
type Field struct {
	In    Location    `json:"in"`
	By    Location    `json:"by"`
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
	When  time.Time   `json:"when"`
}

type sortedFields []Field

func (s sortedFields) Len() int {
	return len(s)
}

func (s sortedFields) Less(i, j int) bool {
	return s[i].Key < s[j].Key
}

func (s sortedFields) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

//**************************************************************
// Collection
//**************************************************************

// Collection represents a giving set where a giving value has
// a series of collated values.
type Collection struct {
	Name  string        `json:"name"`
	When  time.Time     `json:"when"`
	Items []interface{} `json:"items"`
	In    Location      `json:"in"`
	By    Location      `json:"by"`
}

//**************************************************************
// Status struct.
//**************************************************************

// Progress embodies giving messages where giving progress with
// associated message and level for status.
type Status struct {
	When    time.Time   `json:"when"`
	Message string      `json:"message"`
	Level   Level       `json:"level"`
	Err     interface{} `json:"err"`
	In      Location    `json:"in"`
	By      Location    `json:"by"`
}

//**************************************************************
// Compute struct.
//**************************************************************

// Metric is the result of a computation.
type Metric struct {
	Title string      `json:"title"`
	Value interface{} `json:"title"`
	Meta  interface{} `json:"meta"`
}

// Compute defines an interface which exposes a method to get
// the title of giving computation with computed value.
type Compute interface {
	Compute() Metric
}

//**************************************************************
// BugLog struct.
//**************************************************************

// BugLog represent a giving record of data at a giving period of time.
type BugLog struct {
	Title      string                `json:"title"`
	Signature  string                `json:"signature"`
	To         time.Time             `json:"to"`
	From       time.Time             `json:"from"`
	Tags       []string              `json:"tags"`
	Fields     []Field               `json:"fields"`
	Progress   []Status              `json:"status"`
	Metrics    []Metric              `json:"metrics"`
	Collection map[string]Collection `json:"collection"`

	bugs     Handler
	computes []Compute
	has      map[string]int
}

// FromFields returns a new instance of BugLog from source but unique
// and sets giving fields.
func (b *BugLog) FromFields(kv map[string]interface{}) Ctx {
	return b.branch().WithFields(kv)
}

// FromKV returns a new instance of BugLog from source but unique
// and adds key-value pair into Ctx.
func (b *BugLog) FromKV(k string, v interface{}) Ctx {
	return b.branch().With(k, v)
}

// FromTags returns a new instance of BugLog from source but unique
// and sets giving tags.
func (b *BugLog) FromTags(tags ...string) Ctx {
	return b.branch().WithTags(tags...)
}

// FromTitle returns a new instance of BugLog from source but unique
// and sets giving title.
func (b *BugLog) FromTitle(title string, v ...interface{}) Ctx {
	return b.branch().WithTitle(title, v...)
}

// branch duplicates BugLog and copies appropriate
// dataset over to ensure uniqueness of values to source.
func (b *BugLog) branch() Ctx {
	br := *b

	br.From = time.Now()
	br.Signature = randName(20)
	br.has = make(map[string]int)
	for k, ind := range b.has {
		br.has[k] = ind
	}

	br.computes = make([]Compute, len(b.computes))
	copy(br.computes, b.computes)

	br.Tags = make([]string, len(b.Tags))
	copy(br.Tags, b.Tags)

	br.Fields = make([]Field, len(b.Fields))
	copy(br.Fields, b.Fields)

	br.Progress = make([]Status, len(b.Progress))
	copy(br.Progress, b.Progress)

	br.Metrics = make([]Metric, len(b.Metrics))
	copy(br.Metrics, b.Metrics)

	return &br
}

// Done sets giving B instance into underline Handler instance.
func (b *BugLog) Done() error {
	b.To = time.Now()
	if b.Signature == "" {
		b.Signature = randName(20)
	}

	for _, compute := range b.computes {
		b.Metrics = append(b.Metrics, compute.Compute())
	}

	sort.Sort(sortedFields(b.Fields))

	var err error
	if b.bugs != nil {
		err = b.bugs.Recv(b)
		b.Signature = randName(20)
		b.Progress = make([]Status, 10)
	}

	return err
}

// Collect adds a giving series of items into a collection with associated
// values.
func (b *BugLog) Collect(kv string, items ...interface{}) Ctx {
	if b.Collection == nil {
		b.Collection = make(map[string]Collection)
	}

	if col, ok := b.Collection[kv]; ok {
		col.Items = append(col.Items, items...)
		b.Collection[kv] = col
		return b
	}

	b.Collection[kv] = Collection{
		Name:  kv,
		Items: items,
		When:  time.Now(),
		In:    makeLocation(5),
		By:    makeLocation(4),
	}

	return b
}

// Red logs giving status message at giving time with RedLvl.
func (b *BugLog) Red(msg string, vals ...interface{}) Ctx {
	if len(vals) != 0 {
		msg = fmt.Sprintf(msg, vals...)
	}

	return b.logStatus(Status{
		Message: msg,
		Level:   RedLvl,
		When:    time.Now(),
		In:      makeLocation(5),
		By:      makeLocation(4),
	})
}

// Yellow logs giving status message at giving time with YellowLvl.
func (b *BugLog) Yellow(msg string, vals ...interface{}) Ctx {
	if len(vals) != 0 {
		msg = fmt.Sprintf(msg, vals...)
	}

	return b.logStatus(Status{
		Message: msg,
		Level:   YellowLvl,
		When:    time.Now(),
		In:      makeLocation(5),
		By:      makeLocation(4),
	})
}

// Error logs giving status message at giving time with ErrorLvl.
func (b *BugLog) Error(err error, msg string, vals ...interface{}) Ctx {
	if len(vals) != 0 {
		msg = fmt.Sprintf(msg, vals...)
	}

	return b.logStatus(Status{
		Err:     err,
		Message: msg,
		Level:   ErrorLvl,
		When:    time.Now(),
		In:      makeLocation(5),
		By:      makeLocation(4),
	})
}

// Info logs giving status message at giving time with InfoLvl.
func (b *BugLog) Info(msg string, vals ...interface{}) Ctx {
	if len(vals) != 0 {
		msg = fmt.Sprintf(msg, vals...)
	}

	return b.logStatus(Status{
		Message: msg,
		Level:   InfoLvl,
		When:    time.Now(),
		In:      makeLocation(5),
		By:      makeLocation(4),
	})
}

func (b *BugLog) logStatus(s ...Status) Ctx {
	for _, sl := range s {
		if sl.Err == nil {
			if sl.Level == YellowLvl {
				sl.Err = ErrYellowAlert
			}
			if sl.Level == RedLvl {
				sl.Err = ErrRedAlert
			}
		}
		b.Progress = append(b.Progress, sl)
	}
	return b
}

// WithFields returns giving Field after adding fields to B.
func (b *BugLog) WithFields(fields map[string]interface{}) Ctx {
	if b.has == nil {
		b.has = make(map[string]int)
	}

	for k, v := range fields {
		if ind, ok := b.has[k]; ok {
			b.Fields[ind].Value = v
			b.Fields[ind].When = time.Now()
			continue
		}

		b.has[k] = len(b.Fields)
		b.Fields = append(b.Fields, Field{
			Key:   k,
			Value: v,
			When:  time.Now(),
			In:    makeLocation(5),
			By:    makeLocation(4),
		})
	}
	return b
}

// With sets giving key and valid pair into the field list.
func (b *BugLog) With(id string, val interface{}) Ctx {
	if b.has == nil {
		b.has = make(map[string]int)
	}

	if ind, ok := b.has[id]; ok {
		b.Fields[ind].Value = val
		b.Fields[ind].When = time.Now()
		return b
	}

	b.has[id] = len(b.Fields)
	b.Fields = append(b.Fields, Field{
		Key:   id,
		Value: val,
		When:  time.Now(),
		In:    makeLocation(5),
		By:    makeLocation(4),
	})
	return b
}

// WithTitle sets the title of the giving B
func (b *BugLog) WithTitle(title string, v ...interface{}) Ctx {
	if len(v) != 0 {
		title = fmt.Sprintf(title, v...)
	}

	b.Title = title
	return b
}

// WithCompute adds giving computation into giving bug.
func (b *BugLog) WithCompute(c Compute) Ctx {
	b.computes = append(b.computes, c)
	return b
}

// WithTags adds giving tag value into tags slice.
func (b *BugLog) WithTags(tag ...string) Ctx {
	b.Tags = append(b.Tags, tag...)
	return b
}

//**************************************************************
// Source Interface
//**************************************************************

var _ = &BugLog{}

// Ctx exposes an interface which provides a means to collate giving
// data for a giving data.
type Ctx interface {
	Done() error
	WithTags(...string) Ctx
	WithCompute(Compute) Ctx
	With(string, interface{}) Ctx
	Red(string, ...interface{}) Ctx
	Info(string, ...interface{}) Ctx
	Yellow(string, ...interface{}) Ctx
	Collect(string, ...interface{}) Ctx
	WithTitle(string, ...interface{}) Ctx
	WithFields(map[string]interface{}) Ctx
	Error(error, string, ...interface{}) Ctx

	// FromTags returns a new Ctx unique from source with provided tags.
	FromTags(...string) Ctx

	// FromKV return a new Ctx unique from source adding giving key and value
	// to new one.
	FromKV(string, interface{}) Ctx

	// FromTitle returns a new Ctx unique from source with provided title.
	FromTitle(string, ...interface{}) Ctx

	// FromFields returns a new Ctx unique from source with provided fields.
	FromFields(map[string]interface{}) Ctx
}

// Sources exposes a single method to deliver giving B value to
type Source interface {
	FromTags(...string) Ctx
	WithHandlers(h ...Handler) Ctx
	FromTitle(string, ...interface{}) Ctx
	FromFields(map[string]interface{}) Ctx
}

//**************************************************************
// internal methods and impl
//**************************************************************

func makeLocation(d int) Location {
	var loc Location
	loc.Function, loc.File, loc.Line = GetMethod(d)
	return loc
}

// We omit vowels from the set of available characters to reduce the chances
// of "bad words" being formed.
var alphanums = []rune("bcdfghjklmnpqrstvwxz0123456789")

// String generates a random alphanumeric string, without vowels, which is n
// characters long.  This will panic if n is less than zero.
func randName(length int) string {
	b := make([]rune, length)
	for i := range b {
		b[i] = alphanums[rand.Intn(len(alphanums))]
	}
	return string(b)
}
