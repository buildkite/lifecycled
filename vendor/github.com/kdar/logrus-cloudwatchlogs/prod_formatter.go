package logrus_cloudwatchlogs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/osext"
	"github.com/sirupsen/logrus"
)

type ProdFormatter struct {
	hostname                string
	appname                 string
	httpRequestKey          string
	httpRequestHeaderFilter []string
}

type ProdFormatterOption func(*ProdFormatter) error

//Marshaler is an interface any type can implement to change its output in our production logs.
type Marshaler interface {
	MarshalLog() map[string]interface{}
}

// Hostname is a formatter option that specifies the hostname this
// program is running on. If this is not specified, the system's hostname
// will be used.
func Hostname(name string) ProdFormatterOption {
	return func(f *ProdFormatter) error {
		f.hostname = name
		return nil
	}
}

// AppName is a formatter option that specifies the name of the app.
// If this is not specified, the default is to use the executable name.
func AppName(name string) ProdFormatterOption {
	return func(f *ProdFormatter) error {
		f.appname = name
		return nil
	}
}

// HTTPRequest is a formatter option that allows you to indicate
// that a certain key will contain an *http.Request. If it does, it
// will be formatted in the output. You can provide an optional list
// of keys to filter out of the serialized header. This is useful so
// you don't include sensitive information (like the authorization header).
// Note: if you do not provide this and you pass in an *http.Request, it will
// fail because encoding/json cannot serialize *http.Request.
func HTTPRequest(key string, headerFilter ...string) ProdFormatterOption {
	return func(f *ProdFormatter) error {
		f.httpRequestKey = key
		f.httpRequestHeaderFilter = headerFilter
		return nil
	}
}

// NewProdFormatter creates a new cloudwatchlogs production formatter.
// This is opinionated and you can feel free to create your own.
func NewProdFormatter(options ...ProdFormatterOption) *ProdFormatter {
	f := &ProdFormatter{}

	for _, option := range options {
		option(f)
	}

	var err error
	if f.hostname == "" {
		if f.hostname, err = os.Hostname(); err != nil {
			f.hostname = "unknown"
		}
	}

	if f.appname == "" {
		if f.appname, err = osext.Executable(); err == nil {
			f.appname = filepath.Base(f.appname)
		} else {
			f.appname = "app"
		}
	}

	return f
}

// Format formats logrus.Entry in the form of:
// [timestamp] [jsondata]
func (f *ProdFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	//tmp := make([]byte, 50)
	b := &bytes.Buffer{}

	// //b.WriteString(time.Now().Format("2006-01-02T15:04:05.999Z"))
	now := time.Now()
	// year, month, day := now.Date()
	// hour, minute, second := now.Clock()
	// nano := now.Nanosecond()
	// fourDigits(&tmp, 0, year)
	// tmp[4] = '-'
	// twoDigits(&tmp, 5, int(month))
	// tmp[7] = '-'
	// twoDigits(&tmp, 8, day)
	// tmp[10] = 'T'
	// twoDigits(&tmp, 11, hour)
	// tmp[13] = ':'
	// twoDigits(&tmp, 14, minute)
	// tmp[16] = ':'
	// twoDigits(&tmp, 17, second)
	// tmp[19] = '.'
	// threeDigits(&tmp, 20, nano)
	// tmp[23] = 'Z'
	// b.Write(tmp[:24])
	//
	// b.WriteRune(' ')

	// // This is so incredibly hacky. Needed until logrus implements
	// // this.
	// skip := 8
	// if len(entry.Data) == 0 {
	// 	skip = 9
	// }
	// file, line, function := fileInfo(skip)

	data := map[string]interface{}{
		"time":  now.Unix(), //b.String()[:24],
		"msg":   entry.Message,
		"level": entry.Level.String(),
		"host":  f.hostname,
		// "file":  file,
		// "line":  line,
		// "func":  function,
		"app": f.appname,
	}

	for k, v := range entry.Data {
		switch v := v.(type) {
		case error:
			// Otherwise errors are ignored by `encoding/json`
			// https://github.com/sirupsen/logrus/issues/137
			data[k] = v.Error()
		case Marshaler:
			data[k] = v.MarshalLog()
		default:
			data[k] = v
		}
	}

	if v, ok := data[f.httpRequestKey]; ok {
		if req, ok := v.(*http.Request); ok {
			header := make(map[string]interface{})
			for key, value := range req.Header {
				header[key] = value
			}
			for _, key := range f.httpRequestHeaderFilter {
				delete(header, key)
			}

			data[f.httpRequestKey] = map[string]interface{}{
				"method": req.Method,
				// We have to use RequestURI because URL may be
				// modified by routes.
				"url":         req.RequestURI, //t.URL.String(),
				"host":        req.Host,
				"remote_addr": req.RemoteAddr,
				"header":      header,
			}
		}
	}

	j, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	b.Write(j)

	return b.Bytes(), nil
}

func fileInfo(skip int) (string, int, string) {
	function := "<???>"
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		file = "<???>"
		line = 1
	} else {
		slash := strings.LastIndex(file, "/")
		if slash >= 0 {
			file = file[slash+1:]
		}

		me := runtime.FuncForPC(pc)
		if me != nil {
			function = me.Name()
		}
	}

	return file, line, function
}

const ddigits = `0001020304050607080910111213141516171819` +
	`2021222324252627282930313233343536373839` +
	`4041424344454647484950515253545556575859` +
	`6061626364656667686970717273747576777879` +
	`8081828384858687888990919293949596979899`

// itoa converts an integer d to its ascii representation
// i is the deintation index in buf
// algorithm from https://www.facebook.com/notes/facebook-engineering/three-optimization-tips-for-c/10151361643253920
func itoa(buf *[]byte, i, d int) int {
	j := len(*buf)

	for d >= 100 {
		// Integer division is slow, so we do it by 2
		index := (d % 100) * 2
		d /= 100
		j--
		(*buf)[j] = ddigits[index+1]
		j--
		(*buf)[j] = ddigits[index]
	}

	if d < 10 {
		j--
		(*buf)[j] = byte(int('0') + d)
		return copy((*buf)[i:], (*buf)[j:])
	}

	index := d * 2
	j--
	(*buf)[j] = ddigits[index+1]
	j--
	(*buf)[j] = ddigits[index]

	return copy((*buf)[i:], (*buf)[j:])
}

const digits = "0123456789"

// twoDigits converts an integer d to its ascii representation
// i is the destination index in buf
func twoDigits(buf *[]byte, i, d int) {
	(*buf)[i+1] = digits[d%10]
	d /= 10
	(*buf)[i] = digits[d%10]
}

func threeDigits(buf *[]byte, i, d int) {
	(*buf)[i+2] = digits[d%10]
	d /= 10
	(*buf)[i+1] = digits[d%10]
	d /= 10
	(*buf)[i] = digits[d%10]
}

func fourDigits(buf *[]byte, i, d int) {
	(*buf)[i+3] = digits[d%10]
	d /= 10
	(*buf)[i+2] = digits[d%10]
	d /= 10
	(*buf)[i+1] = digits[d%10]
	d /= 10
	(*buf)[i] = digits[d%10]
}
