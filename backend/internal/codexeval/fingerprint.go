package codexeval

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Each run sends fingerprintTarget challenges, at most that many at once per credential and
// fingerprintGlobalConcurrency across credentials. A challenge holds its slot through its retries.
const (
	fingerprintTarget             = 3
	fingerprintGlobalConcurrency  = 6
	fingerprintMaxText            = 64000
	fingerprintDefaultConcurrency = fingerprintTarget
	fingerprintMaxConcurrency     = fingerprintTarget
)

type fingerprintChallenge struct {
	Instructions  string `json:"instructions,omitempty"`
	Prompt        string `json:"prompt"`
	ExpectedCount int    `json:"expected_count"`
}

//go:embed data/lm-detector/enrollment-suite.json
var fingerprintSuiteJSON []byte

// fingerprintSuite holds the challenges the reference answers were collected with: 12 prompt environments that
// add a system prompt, a user prefix or nothing to their three challenges.
var fingerprintSuite = func() []fingerprintChallenge {
	var suite []fingerprintChallenge
	for _, c := range mustDecode[[]struct {
		System        string `json:"system"`
		UserPrefix    string `json:"user_prefix"`
		Prompt        string `json:"prompt"`
		ExpectedCount int    `json:"expected_count"`
	}](fingerprintSuiteJSON) {
		prompt := c.Prompt
		if c.UserPrefix != "" {
			prompt = c.UserPrefix + "\n\nFinal task:\n" + c.Prompt
		}
		suite = append(suite, fingerprintChallenge{c.System, prompt, c.ExpectedCount})
	}
	return suite
}()

var selectFingerprintChallenges = fingerprintChallenges

func fingerprintChallenges() []fingerprintChallenge {
	challenges := make([]fingerprintChallenge, fingerprintTarget)
	for i, j := range rand.Perm(len(fingerprintSuite))[:fingerprintTarget] {
		challenges[i] = fingerprintSuite[j]
	}
	return challenges
}

// fingerprintAliases maps tested models to the candidate that stands for them: the bank enrolls gpt-6.1-sol
// answers under gpt-6-astra, and deepseek-flash serves the model enrolled as deepseek-v4.1-flash-1017.
// The page compares models with the same table.
var fingerprintAliases = map[string]string{"gpt-6.1-sol": "gpt-6-astra", "deepseek-flash": "deepseek-v4.1-flash-1017"}

// FingerprintModelID is the candidate a model name stands for, without a provider prefix such as "openai/".
func FingerprintModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = model[strings.LastIndex(model, "/")+1:]
	if alias, ok := fingerprintAliases[model]; ok {
		return alias
	}
	return model
}

func fingerprintModelID(model string) string {
	return FingerprintModelID(model)
}

type fingerprintSample struct {
	fingerprintChallenge
	Attempts int    `json:"attempts,omitempty"`
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
	Parsed   int    `json:"parsed_numbers"`
	Valid    bool   `json:"accepted"`
}

// fingerprintResult stores one completed, partial, failed, or cancelled challenge run.
type fingerprintResult struct {
	Info
	ID               string                  `json:"id"`
	Time             time.Time               `json:"time"`
	Model            string                  `json:"model"`
	Status           string                  `json:"status"`
	Error            string                  `json:"error,omitempty"`
	DurationMS       int64                   `json:"duration_ms"`
	InputTokens      int64                   `json:"input_tokens"`
	OutputTokens     int64                   `json:"output_tokens"`
	ReasoningTokens  int64                   `json:"reasoning_tokens"`
	DetectorRevision string                  `json:"detector_revision"`
	Concurrency      int                     `json:"concurrency,omitempty"`
	Samples          []fingerprintSample     `json:"samples"`
	Attribution      *fingerprintAttribution `json:"attribution,omitempty"`
}

// summary drops the samples and rankings that only the detail dialog shows.
func (r fingerprintResult) summary() fingerprintResult {
	r.Samples = nil
	if r.Attribution != nil {
		attribution := *r.Attribution
		attribution.Results, attribution.Diagnostics = nil, nil
		r.Attribution = &attribution
	}
	return r
}

type fingerprintProgress struct {
	Model       string `json:"model"`
	Done        int    `json:"done"`
	Total       int    `json:"total"`
	Concurrency int    `json:"concurrency"`
}

var (
	fingerprintSlots   = make(chan struct{}, fingerprintGlobalConcurrency)
	fingerprintResults = map[string][]fingerprintResult{}
	fingerprintRunning = map[string]*fingerprintProgress{}
)

func runFingerprint(ctx context.Context, auth credential, model string, p *fingerprintProgress) {
	runFingerprintChecked(ctx, auth, model, p, nil)
}

func runFingerprintChecked(ctx context.Context, auth credential, model string, p *fingerprintProgress, check func() error) {
	started := time.Now()
	done := 0
	stopped := false
	r := fingerprintResult{Info: auth.info(), ID: fmt.Sprint(started.UnixNano()), Time: started.UTC(), Model: model, DetectorRevision: detectorRevision, Concurrency: p.Concurrency}
	r.Status = "completed"
	defer func() {
		if ctx.Err() != nil && done < fingerprintTarget {
			r.Status = "cancelled"
		}
		if recovered := recover(); recovered != nil {
			r.Status, r.Error = "failed", fmt.Sprint(recovered)
		}
		r.DurationMS = time.Since(started).Milliseconds()
		mu.Lock()
		defer mu.Unlock()
		delete(fingerprintRunning, auth.ID)
		if stopped {
			return
		}
		fingerprintResults[auth.ID] = appendRecent(fingerprintResults[auth.ID], r)
		_ = saveStateLocked()
	}()
	challenges := selectFingerprintChallenges()
	type event struct {
		index  int
		sample fingerprintSample
		output modelResponse
		err    error
	}
	jobs, events := make(chan int, fingerprintTarget), make(chan event, fingerprintTarget)
	for i := range challenges {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range p.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				select {
				case fingerprintSlots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				sample, output, err := collectFingerprintSample(ctx, auth, model, challenges[i], check)
				<-fingerprintSlots
				if errors.Is(err, errInspectionStopped) {
					events <- event{i, sample, output, err}
					return
				}
				if sample.Attempts > 0 {
					events <- event{i, sample, output, err}
				}
			}
		}()
	}
	go func() { workers.Wait(); close(events) }()
	samples := make([]*fingerprintSample, fingerprintTarget)
	for e := range events {
		if errors.Is(e.err, errInspectionStopped) {
			stopped = true
			continue
		}
		samples[e.index] = &e.sample
		r.InputTokens += e.output.InputTokens
		r.OutputTokens += e.output.OutputTokens
		r.ReasoningTokens += e.output.ReasoningTokens
		if !errors.Is(e.err, context.Canceled) {
			done++
		}
		mu.Lock()
		p.Done = done
		mu.Unlock()
	}
	if stopped {
		return
	}
	var outputs []fingerprintOutput
	// Preserve challenge order even when requests complete out of order.
	for _, sample := range samples {
		if sample == nil {
			continue
		}
		r.Samples = append(r.Samples, *sample)
		outputs = append(outputs, fingerprintOutput{sample.Text, sample.ExpectedCount})
	}
	attr, err := analyzeFingerprint(outputs)
	switch {
	case err == nil:
		r.Attribution = &attr
		if attr.Used < fingerprintTarget {
			r.Status = "partial"
		}
	case len(r.Samples) > 0 && r.Samples[len(r.Samples)-1].Error != "":
		// No answer was valid; the last request's error tells why.
		r.Status, r.Error = "failed", r.Samples[len(r.Samples)-1].Error
	default:
		r.Status, r.Error = "failed", err.Error()
	}
}

// collectFingerprintSample asks challenge until an answer can be scored. The sample keeps the last answer.
func collectFingerprintSample(ctx context.Context, auth credential, model string, challenge fingerprintChallenge, check func() error) (sample fingerprintSample, out modelResponse, err error) {
	sample.fingerprintChallenge = challenge
	out, sample.Attempts, err = askCodex(ctx, auth, model, "", challenge.Instructions, challenge.Prompt, challengeTimeout, func(answer string) (err error) {
		sample.Text, sample.Parsed, err = fingerprintText(answer, challenge.ExpectedCount)
		return err
	}, check)
	sample.Valid = err == nil
	if err != nil {
		sample.Error = truncate(err.Error(), 500)
	}
	return
}

// fingerprintText returns the text of answer that the detector scores and the count of its numbers, or why
// the answer cannot be scored. Like the upstream detector, only the requested count is scored when an answer runs
// longer.
func fingerprintText(answer string, expected int) (text string, parsed int, err error) {
	if len(answer) > fingerprintMaxText {
		return "", 0, errors.New("回答过长")
	}
	text, numbers := answer, parseNumbers(answer)
	if len(numbers) > expected {
		numbers = numbers[:expected]
		values := make([]string, len(numbers))
		for i, n := range numbers {
			values[i] = strconv.Itoa(n)
		}
		text = strings.Join(values, ", ")
	}
	if len(numbers) < minimumNumbers(expected) {
		err = errors.New("有效数字不足")
	}
	return text, len(numbers), err
}
