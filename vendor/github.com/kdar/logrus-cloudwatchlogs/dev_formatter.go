package logrus_cloudwatchlogs

import (
	"net/http"

	"github.com/sirupsen/logrus"
)

type DevFormatter struct {
	HTTPRequestKey string
	*logrus.TextFormatter
}

func (f *DevFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	if _, ok := entry.Data[f.HTTPRequestKey]; ok {
		req, ok := entry.Data[f.HTTPRequestKey].(*http.Request)
		if ok {
			entry.Data[f.HTTPRequestKey] = req.Method + " " + req.RequestURI
		}
	}

	if f.TextFormatter == nil {
		f.TextFormatter = &logrus.TextFormatter{}
	}

	return f.TextFormatter.Format(entry)
}
