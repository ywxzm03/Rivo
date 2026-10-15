package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ywxzm03/Rivo/internal/app"
	"github.com/ywxzm03/Rivo/internal/llm"
)

func TestNonTUIInputsDoNotInterpretSessionOrSkillCommands(t *testing.T) {
	inputs := []string{"/skill:docs explain this", "/rename Literal name", "/new"}
	var stdout, stderr bytes.Buffer
	var actual []string
	code := runREPL(strings.NewReader(strings.Join(inputs, "\n")+"\n/exit\n"), &stdout, &stderr, func(_ context.Context, input string) error {
		actual = append(actual, input)
		return nil
	}, nil, nil, nil, nil)
	if code != 0 || !reflect.DeepEqual(actual, inputs) {
		t.Fatalf("REPL inputs = %q, code = %d", actual, code)
	}
	for _, input := range inputs {
		runner := &fakeRunner{msg: &llm.AssistantMessage{StopReason: llm.StopReasonStop}}
		if code := printMode(app.Config{}, input, false, &stdout, &stderr, runner); code != 0 || runner.input != input {
			t.Fatalf("print input = %q, code = %d", runner.input, code)
		}
	}
}
