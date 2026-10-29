import { Terminal, Sparkles, Settings2, ArrowDown, Square, X, Check, FileText, FilePenLine, ChevronDown, ArrowUp, Server, Box, Activity, User, TriangleAlert, Plus, RefreshCw, Link, ArrowUpRight } from "lucide-react";
import mark from "./assets/bailu-mark.png";
import wordmark from "./assets/bailu-wordmark.png";

const icons = {terminal: Terminal, spark: Sparkles, settings: Settings2, arrow: ArrowDown, stop: Square, close: X, check: Check, file: FileText, edit: FilePenLine, chevron: ChevronDown, send: ArrowUp, system: Server, box: Box, activity: Activity, user: User, alert: TriangleAlert, plus: Plus, refresh: RefreshCw, link: Link, diagonal: ArrowUpRight};
export type IconName = keyof typeof icons;
export function Icon({ name, size = 20 }: { name: IconName; size?: number }) {
  const Glyph = icons[name];
  return <Glyph size={size} strokeWidth={2} aria-hidden="true" />;
}
export function Logo({ size = 32 }: { size?: number }) {
  return <span className="logo"><img src={mark} width={size} height={size} alt="" /><img src={wordmark} width={128} height={24} alt="BAILU AI" /></span>;
}
