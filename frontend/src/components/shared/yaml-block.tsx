import { cn } from "@/lib/utils";

/** Minimal, dependency-free YAML syntax highlighter for read-only display —
 * keys, strings, numbers/booleans and comments get distinct tones. Good
 * enough for rendered specs and policies; not a general-purpose YAML editor. */
export function YamlBlock({ yaml, className }: { yaml: string; className?: string }) {
  const lines = yaml.split("\n");
  return (
    <pre className={cn("overflow-auto rounded-md bg-surface-sunken p-3 font-mono text-[12px] leading-relaxed", className)}>
      {lines.map((line, i) => (
        <div key={i}>{highlight(line)}</div>
      ))}
    </pre>
  );
}

function highlight(line: string) {
  const commentIdx = line.indexOf("#");
  const code = commentIdx >= 0 ? line.slice(0, commentIdx) : line;
  const comment = commentIdx >= 0 ? line.slice(commentIdx) : "";

  const m = code.match(/^(\s*(?:-\s+)?)([A-Za-z0-9_.-]+)(:)(\s*)(.*)$/);
  if (!m) {
    return (
      <>
        <span className="text-foreground/80">{code}</span>
        {comment && <span className="text-muted-foreground">{comment}</span>}
      </>
    );
  }
  const [, indent, key, colon, sp, rest] = m;
  return (
    <>
      <span>{indent}</span>
      <span className="text-info">{key}</span>
      <span className="text-muted-foreground">{colon}</span>
      <span>{sp}</span>
      <ValueSpan value={rest} />
      {comment && <span className="text-muted-foreground"> {comment}</span>}
    </>
  );
}

function ValueSpan({ value }: { value: string }) {
  if (!value) return null;
  if (/^-?\d+(\.\d+)?$/.test(value)) return <span className="text-chart-3">{value}</span>;
  if (/^(true|false|null|~)$/.test(value)) return <span className="text-chart-4">{value}</span>;
  if (/^["'].*["']$/.test(value)) return <span className="text-success">{value}</span>;
  return <span className="text-foreground/90">{value}</span>;
}
