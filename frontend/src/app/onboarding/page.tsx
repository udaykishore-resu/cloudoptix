"use client";
import * as React from "react";
import { useRouter } from "next/navigation";
import { ArrowUp, Bot, Check, CircleDashed, HelpCircle, Sparkles, User as UserIcon } from "lucide-react";
import { Logo } from "@/components/shared/logo";
import { ProvenanceChip } from "@/components/shared/provenance-chip";
import { YamlBlock } from "@/components/shared/yaml-block";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Progress } from "@/components/ui/progress";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { useStartOnboarding, useOnboardingSummary, useApproveOnboarding, streamReply } from "@/lib/api/onboarding";
import { cn } from "@/lib/utils";
import type { FieldState, OnboardingState, OpenQuestion } from "@/types/api";
import type { Turn } from "@/types/domain";

export default function OnboardingPage() {
  const router = useRouter();
  const start = useStartOnboarding();
  const [conversationId, setConversationId] = React.useState<string | undefined>();
  const [state, setState] = React.useState<OnboardingState | undefined>();
  const [turns, setTurns] = React.useState<Turn[]>([]);
  const [input, setInput] = React.useState("");
  const [streaming, setStreaming] = React.useState(false);
  const [phase, setPhase] = React.useState<"chat" | "review">("chat");
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const started = React.useRef(false);

  React.useEffect(() => {
    if (started.current) return;
    started.current = true;
    start.mutate(undefined, {
      onSuccess: (res) => {
        setConversationId(res.id);
        setState(res.state);
        setTurns([{ role: "assistant", text: res.state.reply ?? "", at: new Date().toISOString() }]);
      },
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
  }, [turns]);

  const send = async (text: string) => {
    if (!text.trim() || !conversationId || streaming) return;
    setInput("");
    setTurns((t) => [...t, { role: "user", text, at: new Date().toISOString() }]);
    setStreaming(true);
    const assistantIdx = turns.length + 1;
    setTurns((t) => [...t, { role: "assistant", text: "", at: new Date().toISOString() }]);
    for await (const evt of streamReply(conversationId, text)) {
      if (evt.kind === "delta") {
        setTurns((t) => t.map((turn, i) => (i === assistantIdx ? { ...turn, text: turn.text + evt.text } : turn)));
      } else {
        setState(evt.state);
        setTurns((t) => t.map((turn, i) => (i === assistantIdx ? { ...turn, text: evt.state.reply ?? turn.text } : turn)));
      }
    }
    setStreaming(false);
  };

  if (phase === "review" && conversationId) {
    return <ReviewScreen conversationId={conversationId} onBackToChat={() => setPhase("chat")} onCancel={() => router.push("/")} />;
  }

  return (
    <div className="mx-auto flex h-screen max-w-6xl flex-col p-4">
      <header className="mb-3 flex items-center justify-between">
        <Logo />
        <div className="flex items-center gap-2">
          <span className="text-xs text-muted-foreground">Setting up your workspace</span>
          <ThemeToggle />
        </div>
      </header>

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-5">
        <Card className="flex min-h-0 flex-col lg:col-span-3">
          <CardHeader className="border-b border-border py-3">
            <CardTitle className="flex items-center gap-1.5 text-sm"><Sparkles className="h-4 w-4 text-primary" /> Onboarding assistant</CardTitle>
          </CardHeader>
          <CardContent className="flex min-h-0 flex-1 flex-col p-4">
            <div ref={scrollRef} className="min-h-0 flex-1 space-y-4 overflow-y-auto pb-2 pr-1">
              {turns.map((t, i) => (
                <div key={i} className={cn("flex gap-2.5", t.role === "user" && "flex-row-reverse")}>
                  <div className={cn("flex h-7 w-7 shrink-0 items-center justify-center rounded-full", t.role === "user" ? "bg-primary text-primary-foreground" : "bg-secondary")}>
                    {t.role === "user" ? <UserIcon className="h-3.5 w-3.5" /> : <Bot className="h-3.5 w-3.5" />}
                  </div>
                  <div className={cn("max-w-lg rounded-lg px-3.5 py-2.5 text-sm", t.role === "user" ? "bg-primary text-primary-foreground" : "border border-border bg-card")}>
                    {t.text || (streaming && i === turns.length - 1 ? "…" : "")}
                  </div>
                </div>
              ))}
            </div>

            {!!state?.suggestions?.length && !streaming && (
              <div className="flex flex-wrap gap-2 py-2">
                {state.suggestions.map((s) => (
                  <button key={s} onClick={() => send(s)} className="focus-ring rounded-full border border-border bg-secondary/40 px-3 py-1.5 text-xs hover:border-border-strong hover:bg-secondary">
                    {s}
                  </button>
                ))}
              </div>
            )}

            <form
              onSubmit={(e) => {
                e.preventDefault();
                send(input);
              }}
              className="flex items-end gap-2 border-t border-border pt-3"
            >
              <Textarea
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    send(input);
                  }
                }}
                placeholder="Type your answer…"
                rows={1}
                className="max-h-32 min-h-[2.5rem] resize-none"
                disabled={!conversationId}
              />
              <Button type="submit" size="icon" disabled={streaming || !input.trim()}>
                <ArrowUp className="h-4 w-4" />
              </Button>
            </form>
          </CardContent>
        </Card>

        <div className="min-h-0 space-y-3 overflow-y-auto lg:col-span-2">
          <Card>
            <CardHeader className="py-3">
              <CardTitle className="text-sm">Specification completeness</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              <div className="flex items-center justify-between text-xs">
                <span className="text-muted-foreground">{state?.completeness ? `${Math.round((state.completeness.score ?? 0) * 100)}%` : "—"}</span>
                {state?.ready_for_review && <Badge variant="success">Ready for review</Badge>}
              </div>
              <Progress value={(state?.completeness?.score ?? 0) * 100} />
              <div className="grid grid-cols-2 gap-2 pt-1 text-[11px]">
                <BucketCount label="Confirmed" count={state?.collected?.length ?? 0} tone="success" />
                <BucketCount label="Inferred" count={state?.inferred?.length ?? 0} tone="info" />
                <BucketCount label="Needs confirmation" count={state?.needs_confirmation?.length ?? 0} tone="warning" />
                <BucketCount label="Unknown" count={state?.unknown?.length ?? 0} tone="muted" />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="py-3"><CardTitle className="text-sm">What CloudOptix knows so far</CardTitle></CardHeader>
            <CardContent className="max-h-64 space-y-1.5 overflow-y-auto">
              {[...(state?.collected ?? []), ...(state?.inferred ?? []), ...(state?.needs_confirmation ?? [])].map((f, i) => <FieldRow key={i} field={f} />)}
              {!state?.collected?.length && <p className="text-xs text-muted-foreground">Nothing captured yet — keep chatting.</p>}
            </CardContent>
          </Card>

          {!!state?.open_questions?.length && (
            <Card>
              <CardHeader className="py-3"><CardTitle className="flex items-center gap-1.5 text-sm"><HelpCircle className="h-3.5 w-3.5" /> Open questions</CardTitle></CardHeader>
              <CardContent className="space-y-1.5">
                {state.open_questions.map((q, i) => <OpenQuestionRow key={i} q={q} />)}
              </CardContent>
            </Card>
          )}

          <Button className="w-full" disabled={!state?.ready_for_review} onClick={() => setPhase("review")}>
            Review specification
          </Button>
        </div>
      </div>
    </div>
  );
}

function BucketCount({ label, count, tone }: { label: string; count: number; tone: "success" | "info" | "warning" | "muted" }) {
  return (
    <div className={cn("rounded-md px-2 py-1.5", tone === "success" && "bg-success/10", tone === "info" && "bg-info/10", tone === "warning" && "bg-warning/10", tone === "muted" && "bg-muted")}>
      <div className={cn("text-sm font-semibold tabular-nums", tone === "success" && "text-success", tone === "info" && "text-info", tone === "warning" && "text-warning", tone === "muted" && "text-muted-foreground")}>{count}</div>
      <div className="text-muted-foreground">{label}</div>
    </div>
  );
}

function FieldRow({ field: f }: { field: FieldState }) {
  return (
    <div className="flex items-start justify-between gap-2 rounded-md border border-border px-2 py-1.5 text-xs">
      <div className="min-w-0">
        <p className="truncate font-medium">{f.label}</p>
        <p className="truncate text-muted-foreground">{f.value ?? "—"}</p>
      </div>
      {f.provenance && <ProvenanceChip provenance={f.provenance} rationale={f.rationale} source={f.source} className="shrink-0" />}
    </div>
  );
}

function OpenQuestionRow({ q }: { q: OpenQuestion }) {
  return (
    <div className="rounded-md border border-border px-2 py-1.5 text-xs">
      <p className="flex items-center gap-1 font-medium">
        <CircleDashed className="h-3 w-3 text-muted-foreground" />
        {q.question}
        {q.required && <Badge variant="warning" className="text-[9px]">required</Badge>}
      </p>
      {q.why && <p className="mt-0.5 text-muted-foreground">{q.why}</p>}
    </div>
  );
}

function ReviewScreen({ conversationId, onBackToChat, onCancel }: { conversationId: string; onBackToChat: () => void; onCancel: () => void }) {
  const router = useRouter();
  const summary = useOnboardingSummary(conversationId);
  const approve = useApproveOnboarding();

  return (
    <div className="mx-auto max-w-5xl p-4 pb-16">
      <header className="mb-4 flex items-center justify-between">
        <Logo />
        <ThemeToggle />
      </header>
      <h1 className="mb-1 text-xl font-semibold">Review your specification</h1>
      <p className="mb-5 text-sm text-muted-foreground">This is what CloudOptix will use to configure cost intelligence, governance defaults and the architecture digital twin.</p>

      {summary.isLoading && <p className="text-sm text-muted-foreground">Loading summary…</p>}
      {summary.data && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <div className="space-y-3">
            {summary.data.sections?.map((s, i) => (
              <Card key={i}>
                <CardHeader className="py-3"><CardTitle className="text-sm">{s.title}</CardTitle></CardHeader>
                <CardContent className="space-y-1.5">
                  {s.fields?.map((f, j) => <FieldRow key={j} field={f} />)}
                  {s.note && <p className="text-xs text-muted-foreground">{s.note}</p>}
                </CardContent>
              </Card>
            ))}

            {!!summary.data.validation?.issues?.length && (
              <Card className="border-warning/40">
                <CardHeader className="py-3"><CardTitle className="text-sm text-warning">Validation issues</CardTitle></CardHeader>
                <CardContent className="space-y-1.5">
                  {summary.data.validation.issues!.map((iss, i) => (
                    <p key={i} className="text-xs text-muted-foreground"><span className="font-mono">{iss.path}</span>: {iss.message}</p>
                  ))}
                </CardContent>
              </Card>
            )}

            <Card>
              <CardHeader className="py-3"><CardTitle className="text-sm">What happens next</CardTitle></CardHeader>
              <CardContent>
                <ol className="list-decimal space-y-1 pl-4 text-xs text-muted-foreground">
                  {summary.data.what_happens_next?.map((step, i) => <li key={i}>{step}</li>)}
                </ol>
              </CardContent>
            </Card>
          </div>

          <div className="space-y-3">
            <Card>
              <CardHeader className="py-3"><CardTitle className="text-sm">cloudoptix.yaml</CardTitle></CardHeader>
              <CardContent>
                <YamlBlock yaml={summary.data.spec_yaml ?? ""} className="max-h-[520px]" />
              </CardContent>
            </Card>
          </div>
        </div>
      )}

      <div className="sticky bottom-0 mt-6 flex flex-wrap gap-2 border-t border-border bg-background/95 py-4 backdrop-blur">
        <Button
          disabled={!summary.data?.can_approve || approve.isPending}
          onClick={() => approve.mutate(conversationId, { onSuccess: () => router.push("/onboarding/connect") })}
        >
          <Check className="h-4 w-4" /> Approve &amp; Connect AWS
        </Button>
        <Button variant="outline" onClick={onBackToChat}>Edit specification</Button>
        <Button variant="outline" onClick={onBackToChat}>Chat to modify</Button>
        <Button variant="ghost" className="text-muted-foreground" onClick={onCancel}>Cancel</Button>
      </div>
      {summary.data?.blocking_reasons?.length ? (
        <p className="mt-2 text-xs text-destructive">Cannot approve yet: {summary.data.blocking_reasons.join("; ")}</p>
      ) : null}
    </div>
  );
}
