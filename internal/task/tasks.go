package task

import "fmt"

type BashTask struct{}

func (BashTask) Name() string { return "BashTask" }
func (BashTask) Type() Type   { return TypeBash }

func (BashTask) Kill(taskID string, store *Store) error {
	task, process, unregister, token, err := store.beginBashKill(taskID)
	if err != nil {
		return err
	}
	if process != nil {
		if err := process.Kill(); err != nil {
			store.restoreBashKill(task, process, token)
			return err
		}
		if err := process.Cleanup(); err != nil {
			return err
		}
	}
	if unregister != nil {
		unregister()
	}
	return nil
}

type AgentTask struct{}

func (AgentTask) Name() string { return "AgentTask" }
func (AgentTask) Type() Type   { return TypeAgent }

func (AgentTask) Kill(taskID string, store *Store) error {
	runtime, abort, unregister, _, err := store.beginAgentKill(taskID)
	if err != nil {
		return err
	}
	if abort != nil {
		abort()
	} else if runtime.Cancel != nil {
		runtime.Cancel()
	}
	if unregister != nil {
		unregister()
	}
	return nil
}

var taskImplementations = map[Type]Task{
	TypeBash:  BashTask{},
	TypeAgent: AgentTask{},
}

func getTaskByType(taskType Type) (Task, bool) {
	task, ok := taskImplementations[taskType]
	return task, ok
}

func StopTask(store *Store, input StopInput) (StopOutput, error) {
	if input.TaskID == "" {
		return StopOutput{}, &StopTaskError{Code: "not_found", Message: "Missing required parameter: task_id"}
	}
	task, ok := store.Task(input.TaskID)
	if !ok {
		return StopOutput{}, &StopTaskError{Code: "not_found", Message: fmt.Sprintf("No task found with ID: %s", input.TaskID)}
	}
	if task.Base().Status != StatusRunning {
		return StopOutput{}, &StopTaskError{Code: "not_running", Message: fmt.Sprintf("Task %s is not running (status: %s)", input.TaskID, task.Base().Status)}
	}
	implementation, ok := getTaskByType(task.Base().Type)
	if !ok {
		return StopOutput{}, &StopTaskError{Code: "unsupported_type", Message: fmt.Sprintf("Unsupported task type: %s", task.Base().Type)}
	}
	if err := implementation.Kill(input.TaskID, store); err != nil {
		return StopOutput{}, err
	}
	command := task.Base().Description
	if bash, ok := task.(*BashState); ok {
		command = bash.Command
	}
	return StopOutput{
		Message: fmt.Sprintf("Successfully stopped task: %s (%s)", input.TaskID, command),
		TaskID:  input.TaskID,
		Type:    task.Base().Type,
		Command: command,
	}, nil
}
