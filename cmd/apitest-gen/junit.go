package main

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/fada4773-sketch/specproof/apitest"
	"github.com/fada4773-sketch/specproof/internal/scenario"
)

// junit is a JUnit XML report, as CI systems (Azure DevOps, GitLab,
// Jenkins) show it in their test tab.
type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Text    string `xml:",chardata"`
}

// junitResult converts the result of a run.
func junitResult(suite string, res *apitest.Result) junitSuite {
	s := junitSuite{Name: suite, Time: fmt.Sprintf("%.3f", res.Summary.Duration.Seconds())}
	for _, c := range res.Cases {
		if c.Precondition {
			continue
		}
		group, name, _ := strings.Cut(c.Name, "/")
		jc := junitCase{Name: name, ClassName: suite + "." + group, Time: fmt.Sprintf("%.3f", c.Duration.Seconds())}
		switch c.Status {
		case apitest.StatusPassed, apitest.StatusDeviation, apitest.StatusTolerated:
		case apitest.StatusSkipped, apitest.StatusNotBuildable:
			jc.Skipped = &junitMessage{Message: c.Message}
			s.Skipped++
		default:
			jc.Failure = &junitMessage{Message: c.Message, Type: string(c.Status), Text: fmt.Sprintf("%s: %s", c.Status, c.Message)}
			s.Failures++
		}
		s.Tests++
		s.Cases = append(s.Cases, jc)
	}
	return s
}

// junitDiff converts the findings of a regression diff: one test per read,
// failed if a finding of it fails.
func junitDiff(suite string, pre *scenario.Snapshot, fs []scenario.Finding) junitSuite {
	s := junitSuite{Name: suite, Time: "0"}
	byStep := map[string][]scenario.Finding{}
	for _, f := range fs {
		byStep[f.Step] = append(byStep[f.Step], f)
	}
	for _, sh := range pre.Steps {
		jc := junitCase{Name: sh.Request, ClassName: suite, Time: "0"}
		var lines []string
		for _, f := range byStep[sh.ID] {
			if f.Fails {
				lines = append(lines, fmt.Sprintf("%s %s: %s → %s (%s)", f.Code, f.Pointer, f.Old, f.New, f.Message))
			}
		}
		if len(lines) > 0 {
			jc.Failure = &junitMessage{Message: lines[0], Type: "REGRESSION", Text: strings.Join(lines, "\n")}
			s.Failures++
		}
		s.Tests++
		s.Cases = append(s.Cases, jc)
	}
	return s
}

func writeJUnit(path string, suites ...junitSuite) error {
	b, err := xml.MarshalIndent(junitSuites{Suites: suites}, "", "  ")
	if err != nil {
		return err
	}
	return scenario.WriteFile(path, append([]byte(xml.Header), append(b, '\n')...))
}
