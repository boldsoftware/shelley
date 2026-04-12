import React from "react";

export interface TodoItem {
  id: string;
  task: string;
  status: string;
}

interface TodoList {
  items: TodoItem[];
}

interface TodoPanelProps {
  todoContent: string;
  minimized: boolean;
  onToggleMinimize: () => void;
  onDismiss: () => void;
}

function StatusIcon({ status }: { status: string }) {
  const size = 14;
  if (status === "completed") {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.5} strokeLinecap="round" strokeLinejoin="round">
        <path d="M5 13l4 4L19 7" />
      </svg>
    );
  }
  if (status === "in-progress") {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="5" fill="currentColor" />
      </svg>
    );
  }
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2}>
      <circle cx="12" cy="12" r="5" />
    </svg>
  );
}

export default function TodoPanel({ todoContent, minimized, onToggleMinimize, onDismiss }: TodoPanelProps) {

  let todoList: TodoList | null = null;
  try {
    if (todoContent.trim()) {
      todoList = JSON.parse(todoContent) as TodoList;
    }
  } catch {
    return null;
  }

  if (!todoList || !todoList.items || todoList.items.length === 0) {
    return null;
  }

  const totalCount = todoList.items.length;
  const completedCount = todoList.items.filter((item) => item.status === "completed").length;
  const allCompleted = completedCount === totalCount;

  return (
    <div className="todo-panel">
      <div className="todo-panel-header">
        <div className="todo-panel-header-left">
          <svg
            className="todo-panel-icon"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth={2}
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M9 11l3 3L22 4" />
            <path d="M21 12v7a2 2 0 01-2 2H5a2 2 0 01-2-2V5a2 2 0 012-2h11" />
          </svg>
          <span>Working...</span>
          <span className="todo-panel-count">
            {completedCount}/{totalCount}
          </span>
        </div>
        <div className="todo-panel-header-right">
          <button
            className="todo-panel-minimize"
            onClick={onToggleMinimize}
            title={minimized ? "Expand" : "Minimize"}
          >
            {minimized ? "+" : "-"}
          </button>
          {allCompleted && (
            <button className="todo-panel-dismiss" onClick={onDismiss} title="Dismiss">
              ×
            </button>
          )}
        </div>
      </div>
      {!minimized && (
        <div className="todo-panel-items">
          {todoList.items.map((item) => (
            <div key={item.id} className={`todo-item todo-item-${item.status}`}>
              <span className="todo-item-icon"><StatusIcon status={item.status} /></span>
              <span className="todo-item-text">{item.task}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
