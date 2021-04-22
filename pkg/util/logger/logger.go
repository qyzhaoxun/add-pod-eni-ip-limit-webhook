package logger

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	log "github.com/cihub/seelog"
)

const (
	envLogLevel    = "TKE_ENI_LOG_LEVEL"
	envLogFilePath = "TKE_ENI_LOG_FILE"
	// logConfigFormat defines the seelog format, with a rolling file
	// writer. We cannot do this in code and have to resort to using
	// LoggerFromConfigAsString as seelog doesn't have a usable public
	// implementation of NewRollingFileWriterTime
	logConfigFormat = `
<seelog type="asyncloop" minlevel="%s">
 <outputs formatid="main">
  <rollingfile filename="%s" type="date" datepattern="2006-01-02" archivetype="none" maxrolls="7" />
 </outputs>
 <formats>
  <format id="main" format="%%UTCDate(2006-01-02T15:04:05Z07:00) [%%LEVEL] %%Msg%%n" />
 </formats>
</seelog>
`

	syncLogConfigFormat = `
<seelog type="sync" minlevel="%s">
 <outputs formatid="main">
  <rollingfile filename="%s" type="date" datepattern="2006-01-02" archivetype="none" maxrolls="7" />
 </outputs>
 <formats>
  <format id="main" format="%%UTCDate(2006-01-02T15:04:05Z07:00) [%%LEVEL] %%Msg%%n" />
 </formats>
</seelog>
`

	stdoutLogConfigFormat = `
<seelog minlevel="%s">
 <outputs formatid="stdout">
  <console />
 </outputs>
 <formats>
  <format id="stdout" format="%%UTCDate(2006-01-02T15:04:05Z07:00) [%%LEVEL] %%Msg%%n" />
 </formats>
</seelog>
`
)

// GetLogFileLocation returns the log file path
func GetLogFileLocation(defaultLogFilePath string) string {
	logFilePath := os.Getenv(envLogFilePath)
	if logFilePath == "" {
		logFilePath = defaultLogFilePath
	}

	return logFilePath
}

// SetupLogger sets up a file logger
func SetupLogger(logFilePath string) {
	logger, err := log.LoggerFromConfigAsString(fmt.Sprintf(logConfigFormat, getLogLevel(), logFilePath))
	if err != nil {
		fmt.Println("Error setting up logger: ", err)
		return
	}
	log.ReplaceLogger(logger)
}

// SetupLogger sets up a file logger
func SetupSyncLogger(logFilePath string) {
	logger, err := log.LoggerFromConfigAsString(fmt.Sprintf(syncLogConfigFormat, getLogLevel(), logFilePath))
	if err != nil {
		fmt.Println("Error setting up logger: ", err)
		return
	}
	log.ReplaceLogger(logger)
}

func getLogLevel() string {
	seelogLevel, ok := log.LogLevelFromString(strings.ToLower(os.Getenv(envLogLevel)))
	if !ok {
		seelogLevel = log.InfoLvl
	}

	return seelogLevel.String()
}

// SetupStdoutLogger sets up a stdout logger
func SetupStdoutLogger() {
	logger, err := log.LoggerFromConfigAsString(fmt.Sprintf(stdoutLogConfigFormat, getLogLevel()))
	if err != nil {
		fmt.Println("Error setting up logger: ", err)
		return
	}
	log.ReplaceLogger(logger)
}

func Fatalf(err error, format string, args ...interface{}) {
	log.Errorf(format, args...)
	log.Flush()
	panic(err)
}

// LogPanic logs the caller tree when a panic occurs.
func LogPanic(r interface{}) {
	callers := getCallers(r)
	if _, ok := r.(string); ok {
		log.Errorf("Observed a panic: %s\n%v", r, callers)
	} else {
		log.Errorf("Observed a panic: %#v (%v)\n%v", r, r, callers)
	}
	log.Flush()
}

func getCallers(r interface{}) string {
	callers := ""
	for i := 0; true; i++ {
		_, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		callers = callers + fmt.Sprintf("%v:%v\n", file, line)
	}

	return callers
}

func LogError(err error) {
	log.Error(err)
}
