"use client";
import * as React from "react";
import Link from "next/link";
import { CheckCircle2, Clipboard, Cloud, ShieldCheck, XCircle } from "lucide-react";
import { Logo } from "@/components/shared/logo";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { useAwsInstructions } from "@/lib/api/onboarding";
import { useVerifyAccount } from "@/lib/api/awsAccounts";
import { cn } from "@/lib/utils";

const SCOPES: { key: "read" | "analyze" | "plan" | "execute"; label: string; required: boolean; description: string }[] = [
  { key: "read", label: "Read", required: true, description: "Discover resources, tags and configuration across the account." },
  { key: "analyze", label: "Analyze", required: true, description: "Pull Cost and Usage Report data and CloudWatch metrics." },
  { key: "plan", label: "Plan", required: false, description: "Dry-run proposed changes without applying them." },
  { key: "execute", label: "Execute", required: false, description: "Apply approved, governed changes — gated by policy." },
];

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = React.useState(false);
  return (
    <Button
      size="sm"
      variant="outline"
      onClick={() => {
        navigator.clipboard?.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? <CheckCircle2 className="h-3.5 w-3.5" /> : <Clipboard className="h-3.5 w-3.5" />}
      {copied ? "Copied" : "Copy"}
    </Button>
  );
}

export default function ConnectAwsPage() {
  const instructions = useAwsInstructions();
  const [scope, setScope] = React.useState<"read" | "analyze" | "plan" | "execute">("read");
  const verify = useVerifyAccount();
  const [verifyResult, setVerifyResult] = React.useState<Awaited<ReturnType<typeof verify.mutateAsync>> | undefined>();

  return (
    <div className="mx-auto max-w-4xl p-4 pb-16">
      <header className="mb-4 flex items-center justify-between">
        <Logo />
        <ThemeToggle />
      </header>
      <h1 className="mb-1 flex items-center gap-2 text-xl font-semibold"><Cloud className="h-5 w-5 text-primary" /> Connect your AWS account</h1>
      <p className="mb-5 text-sm text-muted-foreground">CloudOptix uses a cross-account IAM role with least-privilege, scope-separated permissions — nothing is granted beyond what each capability needs.</p>

      {instructions.isLoading && <p className="text-sm text-muted-foreground">Loading instructions…</p>}
      {instructions.data && (
        <div className="space-y-4">
          <Card>
            <CardHeader><CardTitle className="text-sm">External ID</CardTitle><CardDescription>Required in the role&rsquo;s trust policy to prevent the confused-deputy problem.</CardDescription></CardHeader>
            <CardContent className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-md bg-surface-sunken px-3 py-2 font-mono text-sm">{instructions.data.external_id}</code>
              <CopyButton text={instructions.data.external_id ?? ""} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader><CardTitle className="text-sm">Trusted principal</CardTitle></CardHeader>
            <CardContent className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-md bg-surface-sunken px-3 py-2 font-mono text-xs">{instructions.data.trusted_principal_arn}</code>
              <CopyButton text={instructions.data.trusted_principal_arn ?? ""} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex-row items-center justify-between space-y-0">
              <div>
                <CardTitle className="text-sm">Least-privilege IAM policies</CardTitle>
                <CardDescription>One role per scope — grant only what you need, add more later.</CardDescription>
              </div>
              <Tabs value={scope} onValueChange={(v) => setScope(v as typeof scope)}>
                <TabsList>
                  {SCOPES.map((s) => (
                    <TabsTrigger key={s.key} value={s.key}>{s.label}{s.required && " *"}</TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
            </CardHeader>
            <CardContent className="space-y-2">
              <div className="flex items-center justify-between">
                <p className="text-xs text-muted-foreground">
                  {SCOPES.find((s) => s.key === scope)?.description} Role name: <code className="font-mono">{(instructions.data.role_names as Record<string, string> | undefined)?.[scope]}</code>
                </p>
                <CopyButton text={(instructions.data.policy_documents as Record<string, string> | undefined)?.[scope] ?? ""} />
              </div>
              <pre className="max-h-72 overflow-auto rounded-md bg-surface-sunken p-3 font-mono text-[11px]">{(instructions.data.policy_documents as Record<string, string> | undefined)?.[scope]}</pre>
            </CardContent>
          </Card>

          <Card>
            <CardHeader><CardTitle className="text-sm">Deploy</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div>
                <div className="mb-1 flex items-center justify-between">
                  <p className="text-xs font-medium">CloudFormation</p>
                  <CopyButton text={instructions.data.cloudformation_url ?? ""} />
                </div>
                <a href={instructions.data.cloudformation_url} className="block truncate rounded-md bg-surface-sunken px-3 py-2 font-mono text-[11px] text-primary hover:underline">
                  {instructions.data.cloudformation_url}
                </a>
              </div>
              <div>
                <div className="mb-1 flex items-center justify-between">
                  <p className="text-xs font-medium">Terraform module</p>
                  <CopyButton text={instructions.data.terraform_module ?? ""} />
                </div>
                <pre className="overflow-auto rounded-md bg-surface-sunken p-3 font-mono text-[11px]">{instructions.data.terraform_module}</pre>
              </div>
              <ul className="space-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
                {instructions.data.instructions?.map((line, i) => <li key={i}>· {line}</li>)}
              </ul>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5 text-sm"><ShieldCheck className="h-4 w-4" /> Verify connection</CardTitle>
              <CardDescription>Once deployed, verify that the roles are assumable and carry the expected permissions.</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              <Button
                onClick={async () => {
                  const res = await verify.mutateAsync("acct_shared01");
                  setVerifyResult(res);
                }}
                disabled={verify.isPending}
              >
                {verify.isPending ? "Verifying…" : "Run verification"}
              </Button>
              {verifyResult && (
                <div className={cn("rounded-md border p-3 text-sm", verifyResult.check?.missingActions?.length ? "border-warning/40 bg-warning/5" : "border-success/40 bg-success/5")}>
                  <p className="mb-2 flex items-center gap-1.5 font-medium">
                    {verifyResult.check?.missingActions?.length ? <XCircle className="h-4 w-4 text-warning" /> : <CheckCircle2 className="h-4 w-4 text-success" />}
                    {verifyResult.check?.missingActions?.length ? "Connected with gaps" : "Fully connected"}
                  </p>
                  <div className="flex flex-wrap gap-1.5">
                    {verifyResult.check?.grantedScopes?.map((s) => <Badge key={s} variant="success" className="capitalize">{s} granted</Badge>)}
                  </div>
                  {!!verifyResult.check?.missingActions?.length && (
                    <div className="mt-2">
                      <p className="mb-1 text-xs font-medium text-warning">Missing IAM actions</p>
                      <ul className="space-y-0.5 font-mono text-[11px] text-muted-foreground">
                        {verifyResult.check.missingActions.map((m: string) => <li key={m}>{m}</li>)}
                      </ul>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>

          <div className="flex gap-2 pt-2">
            <Button asChild>
              <Link href="/">Continue to CloudOptix</Link>
            </Button>
            <Button variant="outline" asChild>
              <Link href="/onboarding">Back to onboarding</Link>
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
