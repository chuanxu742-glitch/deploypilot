import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { Icon } from "./Icon";

export function ModelPicker({
  value,
  models,
  disabled,
  onChange,
}: {
  value: string;
  models: string[];
  disabled: boolean;
  onChange: (value: string) => void;
}) {
  const [open, setOpen] = useState(false);
  // Search is separate from the configured value so opening the menu shows every model.
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const container = useRef<HTMLDivElement>(null);
  const listId = useId();
  const visible = models.filter((model) =>
    model.toLowerCase().includes(query.trim().toLowerCase()),
  );
  const optionId = (index: number) => `${listId}-${index}`;

  useEffect(() => {
    if (models.length && input.current?.closest("dialog")?.open) {
      setQuery("");
      setActive(0);
      setOpen(true);
      input.current?.focus();
    }
  }, [models]);
  useEffect(() => {
    if (open && visible.length)
      document
        .getElementById(optionId(active))
        ?.scrollIntoView({ block: "nearest" });
  }, [open, active, query]);
  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);
  useEffect(() => {
    if (!open) return;
    // Dismiss after click delivery: collapsing on blur can move Save before mouse-up.
    const dismiss = (event: MouseEvent) => {
      if (
        event.target instanceof Node &&
        !container.current?.contains(event.target)
      )
        setOpen(false);
    };
    document.addEventListener("click", dismiss);
    return () => document.removeEventListener("click", dismiss);
  }, [open]);

  const show = () => {
    setQuery("");
    setActive(Math.max(0, models.indexOf(value)));
    setOpen(true);
  };
  const choose = (model: string) => {
    onChange(model);
    setOpen(false);
    setQuery("");
    input.current?.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing) return;
    if (event.key === "Escape" && open) {
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      return;
    }
    if (event.key === "Tab") {
      setOpen(false);
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!open) show();
      else if (visible.length)
        setActive(
          (index) =>
            (index + (event.key === "ArrowDown" ? 1 : -1) + visible.length) %
            visible.length,
        );
    } else if (open && (event.key === "Home" || event.key === "End")) {
      event.preventDefault();
      setActive(event.key === "Home" ? 0 : Math.max(0, visible.length - 1));
    } else if (open && event.key === "Enter") {
      event.preventDefault();
      if (visible[active]) choose(visible[active]);
      else setOpen(false);
    }
  };

  return (
    <div className="model-picker" ref={container}>
      <div className="model-control">
        <input
          ref={input}
          id="model"
          role="combobox"
          autoComplete="off"
          aria-autocomplete="list"
          aria-expanded={open && !disabled}
          aria-controls={open && !disabled ? listId : undefined}
          aria-activedescendant={
            open && visible[active] ? optionId(active) : undefined
          }
          value={value}
          placeholder="输入或搜索模型"
          disabled={disabled}
          onClick={() => {
            if (!open) show();
          }}
          onChange={(event) => {
            onChange(event.target.value);
            setQuery(event.target.value);
            setActive(0);
            setOpen(true);
          }}
          onKeyDown={onKeyDown}
        />
        <button
          type="button"
          className="model-toggle"
          disabled={disabled || !models.length}
          aria-label={open ? "收起模型列表" : "展开模型列表"}
          aria-expanded={open && !disabled}
          aria-controls={open && !disabled ? listId : undefined}
          onClick={() => {
            if (open) setOpen(false);
            else show();
            input.current?.focus();
          }}
        >
          <Icon name="chevron" size={16} />
        </button>
      </div>
      {open && !disabled && (
        <div
          className="model-listbox"
          role="listbox"
          aria-label="可用模型"
          id={listId}
        >
          {visible.length ? (
            visible.map((model, index) => (
              <button
                key={model}
                type="button"
                role="option"
                id={optionId(index)}
                aria-selected={model === value}
                data-active={index === active}
                title={model}
                className="model-option"
                tabIndex={-1}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => choose(model)}
              >
                <span>{model}</span>
                {model === value && <Icon name="check" size={16} />}
              </button>
            ))
          ) : (
            <div className="model-no-results" role="status">
              未找到匹配模型
            </div>
          )}
        </div>
      )}
    </div>
  );
}
