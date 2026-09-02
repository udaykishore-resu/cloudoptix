"use client";
import * as React from "react";
import { AlertTriangle, ArrowUp, Bot, ExternalLink, Sparkles, User as UserIcon, Wrench } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { useCopilotSuggestions, streamAsk } from "@/lib/api/copilot";
import { cn } from "@/lib/utils";
import type { Citation, CopilotAnswer, ToolResult } from "@/types/api";

interface ChatMessage {
  id: string;
  role: "user" | "assistant";
  text: string;
  streaming?: boolean;
  answer?: CopilotAnswer;
}

export default function CopilotPage() {
  const suggestions = useCopilotSuggestions();
  const [messages, setMessages] = React.useState<ChatMessage[]>([
    { id: "welcome", role: "assistant", text: "Ask me anything about your cloud spend, architecture or recommendations — I'll cite the underlying resources, cost records and findings behind every answer." },
  ]);
  const [input, setInput] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const abortRef = React.useRef<AbortController | null>(null);

  React.useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
  }, [messages]);

  const ask = async (question: string) => {
    if (!question.trim() || busy) return;
    setInput("");
    setBusy(true);
    const userMsg: ChatMessage = { id: `u_${Date.now()}`, role: "user", text: question };
    const assistantId = `a_${Date.now()}`;
    setMessages((m) => [...m, userMsg, { id: assistantId, role: "assistant", text: "", streaming: true }]);
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      for await (const evt of streamAsk(question, controller.signal)) {
        if (evt.kind === "delta") {
          setMessages((m) => m.map((msg) => (msg.id === assistantId ? { ...msg, text: msg.text + evt.text } : msg)));
        } else {
          setMessages((m) => m.map((msg) => (msg.id === assistantId ? { ...msg, text: evt.answer.answer ?? msg.text, streaming: false, answer: evt.answer } : msg)));
        }
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex h-[calc(100vh-6rem)] flex-col">
      <PageHeader title="AI Cost Copilot" description="Every answer is grounded in your tenant's actual data, with inspectable citations and tool calls." />

      <div ref={scrollRef} className="min-h-0 flex-1 space-y-4 overflow-y-auto pb-2 pr-1">
        {messages.map((m) => (
          <MessageBubble key={m.id} message={m} />
        ))}
        {suggestions.data && messages.length <= 1 && (
          <div className="flex flex-wrap gap-2 pl-9">
            {suggestions.data.map((q) => (
              <button key={q} onClick={() => ask(q)} className="focus-ring rounded-full border border-border bg-secondary/40 px-3 py-1.5 text-xs hover:border-border-strong hover:bg-secondary">
                {q}
              </button>
            ))}
          </div>
        )}
      </div>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          ask(input);
        }}
        className="mt-3 flex items-end gap-2 border-t border-border pt-3"
      >
        <Textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              ask(input);
            }
          }}
          placeholder="Ask about cost, architecture, or a specific recommendation…"
          rows={1}
          className="max-h-32 min-h-[2.5rem] resize-none"
        />
        <Button type="submit" disabled={busy || !input.trim()} size="icon">
          <ArrowUp className="h-4 w-4" />
        </Button>
      </form>
    </div>
  );
}

function MessageBubble({ message }: { message: ChatMessage }) {
  const isUser = message.role === "user";
  return (
    <div className={cn("flex gap-2.5", isUser && "flex-row-reverse")}>
      <div className={cn("flex h-7 w-7 shrink-0 items-center justify-center rounded-full", isUser ? "bg-primary text-primary-foreground" : "bg-secondary text-foreground")}>
        {isUser ? <UserIcon className="h-3.5 w-3.5" /> : <Bot className="h-3.5 w-3.5" />}
      </div>
      <div className={cn("max-w-2xl space-y-2", isUser && "flex flex-col items-end")}>
        <div className={cn("rounded-lg px-3.5 py-2.5 text-sm", isUser ? "bg-primary text-primary-foreground" : "border border-border bg-card")}>
          {message.text || (message.streaming ? <span className="inline-flex gap-1"><Dot /><Dot delay="150ms" /><Dot delay="300ms" /></span> : null)}
        </div>
        {message.answer && !isUser && <AnswerMeta answer={message.answer} />}
      </div>
    </div>
  );
}

function Dot({ delay }: { delay?: string }) {
  return <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-muted-foreground" style={{ animationDelay: delay }} />;
}

function AnswerMeta({ answer }: { answer: CopilotAnswer }) {
  const grounded = answer.grounded !== false;
  return (
    <div className="w-full space-y-2">
      {!grounded && (
        <div className="flex items-start gap-1.5 rounded-md border border-warning/30 bg-warning/10 px-2.5 py-1.5 text-xs text-warning">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>This answer could not be fully tied to your tenant&rsquo;s data — treat it as unverified.{answer.grounding_issues?.length ? ` ${answer.grounding_issues.join(" ")}` : ""}</span>
        </div>
      )}
      {(answer.citations?.length ?? 0) > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[10px] font-medium uppercase text-muted-foreground">Citations</span>
          {answer.citations!.map((c, i) => <CitationChip key={i} citation={c} />)}
        </div>
      )}
      {(answer.tool_calls?.length ?? 0) > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[10px] font-medium uppercase text-muted-foreground">Tool calls</span>
          {answer.tool_calls!.map((t, i) => <ToolChip key={i} tool={t} />)}
        </div>
      )}
      {answer.follow_ups?.length ? (
        <div className="flex flex-wrap gap-1.5 pt-1">
          {answer.follow_ups.map((f, i) => (
            <Badge key={i} variant="secondary" className="cursor-default text-[10px] font-normal">{f}</Badge>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function CitationChip({ citation: c }: { citation: Citation }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button className="focus-ring inline-flex items-center gap-1 rounded-full border border-border bg-secondary/50 px-2 py-0.5 text-[11px] hover:border-border-strong">
          {c.label ?? c.kind}
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-64 text-xs">
        <p className="font-medium">{c.label}</p>
        <p className="mt-0.5 text-muted-foreground">{c.kind} · {c.id}</p>
        {c.value && <p className="mt-1 tabular-nums font-medium">{c.value}</p>}
        {c.url && (
          <a href={c.url} className="mt-1 flex items-center gap-1 text-primary hover:underline">
            Open <ExternalLink className="h-3 w-3" />
          </a>
        )}
      </PopoverContent>
    </Popover>
  );
}

function ToolChip({ tool }: { tool: ToolResult }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button className="focus-ring inline-flex items-center gap-1 rounded-full border border-border bg-secondary/50 px-2 py-0.5 text-[11px] hover:border-border-strong">
          <Wrench className="h-2.5 w-2.5" /> {tool.name}
          {tool.latency_ms !== undefined && <span className="text-muted-foreground">{tool.latency_ms}ms</span>}
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-72 text-xs">
        <p className="mb-1 flex items-center gap-1 font-medium"><Sparkles className="h-3 w-3" /> {tool.name}</p>
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-surface-sunken p-2 font-mono text-[10px]">{JSON.stringify(tool.result, null, 2)}</pre>
        {tool.error && <p className="mt-1 text-destructive">{tool.error}</p>}
      </PopoverContent>
    </Popover>
  );
}
