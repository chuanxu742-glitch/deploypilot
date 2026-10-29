import { useEffect, useRef, useState } from "react";
import { listModels, setLLMConfig, type LLMStatus } from "./api";
import { Icon } from "./Icon";
import { ModelPicker } from "./ModelPicker";

export function Settings({
  status,
  disabled,
  onSaved,
  version,
}: {
  status: LLMStatus | null;
  disabled: boolean;
  onSaved: (status: LLMStatus) => void;
  version?: string;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [apiKey, setApiKey] = useState("");
  const [model, setModel] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [pending, setPending] = useState<"save" | "models" | null>(null);
  const pendingRef = useRef(false);
  const [feedback, setFeedback] = useState<{
    text: string;
    error: boolean;
  } | null>(null);
  useEffect(() => {
    setModel(status?.model ?? "");
  }, [status?.model]);

  const open = () => {
    setModel(status?.model ?? "");
    setApiKey("");
    setFeedback(null);
    dialog.current?.showModal();
  };
  const close = () => {
    dialog.current?.close();
    setApiKey("");
  };
  const request = async (action: "save" | "models") => {
    if (pendingRef.current) return;
    pendingRef.current = true;
    setPending(action);
    setFeedback(null);
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 20_000);
    try {
      if (action === "save") {
        const config: { apiKey?: string; model?: string } = {};
        if (apiKey.trim()) config.apiKey = apiKey.trim();
        if (model.trim()) config.model = model.trim();
        onSaved(await setLLMConfig(config, controller.signal));
        setApiKey("");
        setFeedback({ text: "已保存", error: false });
      } else {
        // /models reads the server's in-memory key, so store a newly entered key first.
        if (apiKey.trim()) {
          onSaved(await setLLMConfig({ apiKey: apiKey.trim() }, controller.signal));
          setApiKey("");
        }
        const result = await listModels(controller.signal);
        setModels(result);
        setFeedback({
          text: result.length
            ? `已获取 ${result.length} 个模型`
            : "暂无可用模型，请手动输入。",
          error: false,
        });
      }
    } catch (error) {
      setFeedback({ text: controller.signal.aborted ? "请求超时，请重试。" : String(error), error: true });
    } finally {
      clearTimeout(timeout);
      pendingRef.current = false;
      setPending(null);
    }
  };
  return (
    <>
      <button
        type="button"
        className="model-button"
        data-attention={status !== null && !status.hasKey}
        aria-label="模型设置"
        onClick={open}
        disabled={disabled}
        title={disabled ? "任务结束后可修改设置" : "模型设置"}
      >
        <Icon name="settings" size={16} />
        <span>{status?.hasKey ? status.model : "配置模型"}</span>
        <Icon name="chevron" size={14} />
      </button>
      <dialog
        ref={dialog}
        className="settings-dialog"
        aria-labelledby="settings-title"
        onClose={() => setApiKey("")}
        onClick={(event) => {
          if (event.target === dialog.current) close();
        }}
      >
        <div className="dialog-content">
          <header className="dialog-head">
            <div>
              <h2 id="settings-title">模型设置</h2>
            </div>
            <button
              className="icon-button"
              aria-label="关闭设置"
              onClick={close}
            >
              <Icon name="close" />
            </button>
          </header>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void request("save");
            }}
          >
            <label htmlFor="api-key">API Key</label>
            <input
              id="api-key"
              autoComplete="off"
              type="password"
              value={apiKey}
              placeholder={status?.hasKey ? "已配置，留空保留" : "输入 API Key"}
              onChange={(event) => setApiKey(event.target.value)}
              disabled={!!pending}
            />
            <label htmlFor="model">模型名称</label>
            <div className="model-row">
              <ModelPicker
                value={model}
                onChange={setModel}
                models={models}
                disabled={!!pending}
              />
              <button
                type="button"
                className="button secondary"
                onClick={() => void request("models")}
                disabled={!!pending || (!status?.hasKey && !apiKey.trim())}
              >
                {pending === "models" ? "获取中…" : "获取列表"}
              </button>
            </div>
            {feedback && (
              <p
                className={
                  feedback.error ? "feedback error" : "feedback success"
                }
                role="status"
              >
                {feedback.text}
              </p>
            )}
            <footer className="dialog-footer">
              {version && <span className="settings-version">DeployPilot {version}</span>}
              <button
                className="button primary"
                disabled={!!pending || (!status?.hasKey && !apiKey.trim())}
              >
                {pending === "save" ? "保存中…" : "保存设置"}
              </button>
            </footer>
          </form>
        </div>
      </dialog>
    </>
  );
}
