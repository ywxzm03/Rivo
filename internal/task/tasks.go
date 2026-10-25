package task

import "fmt"

// KillBash 终止后台 bash 任务:先杀进程,失败则回滚状态
func KillBash(taskID string, store *Store) error {
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

// KillAgent 取消 agent 任务的 lifecycle 并执行清理回调
func KillAgent(taskID string, store *Store) error {
	runtime, abort, unregister, _, err := store.beginAgentKill(taskID)
	if err != nil {
		return err
	}
	if abort != nil {
		abort()
	} else if runtime != nil && runtime.Cancel != nil {
		// 启动窗口内 runtime 还没挂上,状态已转移为 killed,没有可取消的句柄
		runtime.Cancel()
	}
	if unregister != nil {
		unregister()
	}
	return nil
}

// StopTask 按任务类型分派终止逻辑
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
	switch task.Base().Type {
	case TypeBash:
		if err := KillBash(input.TaskID, store); err != nil {
			return StopOutput{}, err
		}
	case TypeAgent:
		if err := KillAgent(input.TaskID, store); err != nil {
			return StopOutput{}, err
		}
	default:
		return StopOutput{}, &StopTaskError{Code: "unsupported_type", Message: fmt.Sprintf("Unsupported task type: %s", task.Base().Type)}
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
