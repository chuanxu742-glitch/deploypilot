import {
  Terminal,
  Sparkles,
  Settings2,
  ArrowDown,
  Square,
  X,
  Check,
  FileText,
  FilePenLine,
  ChevronDown,
  Server,
  Box,
  Activity,
  User,
  TriangleAlert,
  Plus,
  RefreshCw,
  Link,
  ArrowUpRight,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";

const icons = {
  terminal: Terminal,
  spark: Sparkles,
  settings: Settings2,
  arrow: ArrowDown,
  stop: Square,
  close: X,
  check: Check,
  file: FileText,
  edit: FilePenLine,
  chevron: ChevronDown,
  system: Server,
  box: Box,
  activity: Activity,
  user: User,
  alert: TriangleAlert,
  plus: Plus,
  refresh: RefreshCw,
  link: Link,
  diagonal: ArrowUpRight,
  sidebarClose: PanelLeftClose,
  sidebarOpen: PanelLeftOpen,
};
export type IconName = keyof typeof icons;
export function Icon({ name, size = 20 }: { name: IconName; size?: number }) {
  const Glyph = icons[name];
  return <Glyph size={size} strokeWidth={2} aria-hidden="true" />;
}
