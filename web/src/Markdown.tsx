import {
  Children,
  isValidElement,
  memo,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import "./Markdown.css";

// Markdown is untrusted model output. URL parsing also normalizes embedded
// tabs/newlines before the protocol check; raw HTML never enters the renderer.
function safeUrl(value: string): string {
  try {
    const url = new URL(value, "https://deploypilot.invalid/");
    return ["http:", "https:", "mailto:"].includes(url.protocol)
      ? value.trim()
      : "";
  } catch {
    return "";
  }
}

function MarkdownLink({
  href,
  title,
  children,
}: {
  href?: string;
  title?: string;
  children?: ReactNode;
}) {
  const destination = href ? safeUrl(href) : "";
  return destination ? (
    <a
      href={destination}
      title={title}
      target={destination.startsWith("#") ? undefined : "_blank"}
      rel="noopener noreferrer"
    >
      {children}
    </a>
  ) : (
    <span>{children}</span>
  );
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const child = Children.toArray(children)[0];
  const element = isValidElement<{
    children?: ReactNode;
    className?: string;
  }>(child)
    ? child
    : null;
  const language = /\blanguage-(\S+)/.exec(element?.props.className ?? "")?.[1];
  // mdast's code handler adds one display newline. Remove only that newline,
  // keeping the code's own indentation and trailing blank lines when copying.
  const code = String(element?.props.children ?? "").replace(/\n$/, "");
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">(
    "idle",
  );
  useEffect(() => setCopyState("idle"), [code]);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopyState("copied");
    } catch {
      setCopyState("failed");
    }
  };
  return (
    <div className="markdown-code-block">
      <div className="markdown-code-head">
        <span title={language}>{language || "代码"}</span>
        <button type="button" onClick={copy} aria-label="复制代码">
          {copyState === "copied" ? "已复制" : "复制代码"}
        </button>
      </div>
      <pre>
        <code className={element?.props.className}>{code}</code>
      </pre>
      {copyState !== "idle" && (
        <div
          className="markdown-copy-status"
          data-error={copyState === "failed"}
          role="status"
        >
          {copyState === "failed"
            ? "复制失败，请选中代码手动复制。"
            : "代码已复制。"}
        </div>
      )}
    </div>
  );
}

const components: Components = {
  a: MarkdownLink,
  pre: CodeBlock,
  // Images become explicit links. An assistant response must never initiate
  // an external image request before the operator chooses to open it.
  img: ({ src, alt }) => (
    <span className="markdown-image-link">
      <MarkdownLink href={typeof src === "string" ? src : undefined}>
        查看图片{alt ? `：${alt}` : ""}
      </MarkdownLink>
    </span>
  ),
  table: ({ children }) => (
    <div
      className="markdown-table-scroll"
      role="region"
      aria-label="表格"
      tabIndex={0}
    >
      <table>{children}</table>
    </div>
  ),
};
const remarkPlugins = [remarkGfm];

export const Markdown = memo(function Markdown({
  children,
}: {
  children: string;
}) {
  return (
    <div className="agent-markdown">
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        skipHtml
        urlTransform={safeUrl}
        components={components}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
});
