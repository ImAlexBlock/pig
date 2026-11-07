package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alexblock/pig/internal/store"
)

const jobSchema = `{
  "type": "object",
  "properties": {
    "action":          {"type": "string", "enum": ["list", "status", "wait", "cancel", "logs"]},
    "id":              {"type": "string", "description": "Job id."},
    "timeout_seconds": {"type": "integer", "description": "Wait timeout (max 60)."}
  },
  "required": ["action"],
  "additionalProperties": false
}`

type jobTool struct{}

func (jobTool) Name() string { return "job" }
func (jobTool) Description() string {
	return "Inspect, wait for, cancel or read logs of background jobs."
}
func (jobTool) Schema() json.RawMessage { return json.RawMessage(jobSchema) }
func (jobTool) ReadOnly() bool          { return true }

type jobArgs struct {
	Action         string `json:"action"`
	ID             string `json:"id"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (jobTool) Run(ctx context.Context, c Call, env *Env) (Result, error) {
	var a jobArgs
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return Error("job: invalid arguments: " + err.Error()), nil
	}
	if env.Jobs == nil {
		return Error("job: background jobs are unavailable"), nil
	}
	switch a.Action {
	case "list":
		jobs, err := env.Jobs.ListJobs("", 20)
		if err != nil {
			return Error(fmt.Sprintf("job: %v", err)), nil
		}
		if len(jobs) == 0 {
			return Result{Text: "no jobs", Useless: true, Display: "job list"}, nil
		}
		var sb strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&sb, "%s %s %s (created %s)\n", j.ID, j.Kind, j.Status, time.Unix(j.CreatedAt, 0).Format("15:04:05"))
		}
		return Result{Text: strings.TrimRight(sb.String(), "\n"), Display: "job list"}, nil
	case "status":
		if a.ID == "" {
			return Error("job: id is required"), nil
		}
		j, err := env.Jobs.GetJob(a.ID)
		if err != nil {
			return Error(fmt.Sprintf("job: %v", err)), nil
		}
		return Result{Text: FormatJob(j), Display: "job " + j.ID}, nil
	case "wait":
		if a.ID == "" {
			return Error("job: id is required"), nil
		}
		timeout := a.TimeoutSeconds
		if timeout <= 0 {
			timeout = 60
		}
		if timeout > 60 {
			timeout = 60
		}
		j, err := env.Jobs.WaitJob(ctx, a.ID, time.Duration(timeout)*time.Second)
		if err != nil {
			return Error(fmt.Sprintf("job: %v", err)), nil
		}
		return Result{Text: FormatJob(j), Display: "job " + j.ID}, nil
	case "cancel":
		if a.ID == "" {
			return Error("job: id is required"), nil
		}
		if err := env.Jobs.CancelJob(a.ID); err != nil {
			return Error(fmt.Sprintf("job: %v", err)), nil
		}
		return Result{Text: "canceled " + a.ID, Display: "job cancel"}, nil
	case "logs":
		if a.ID == "" {
			return Error("job: id is required"), nil
		}
		text, err := env.Jobs.JobLogs(a.ID, 50)
		if err != nil {
			return Error(fmt.Sprintf("job: %v", err)), nil
		}
		return Result{Text: text, Display: "job logs " + a.ID}, nil
	}
	return Error(fmt.Sprintf("job: unknown action %q", a.Action)), nil
}

// FormatJob renders one job for the model.
func FormatJob(j *store.Job) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s %s", j.ID, j.Kind, j.Status)
	if j.Error != "" {
		sb.WriteString("\nerror: " + j.Error)
	}
	if j.Result != "" {
		sb.WriteString("\nresult:\n" + j.Result)
	}
	return sb.String()
}
