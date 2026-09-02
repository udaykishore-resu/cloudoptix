"use client";
import * as React from "react";
import { Bell, Building2, CloudCog, History, ShieldCheck, UserPlus, Users } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { QueryBoundary } from "@/components/shared/states";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useTenant, useUsers, useNotificationChannels, useSpecVersions, useInviteUser } from "@/lib/api/tenants";
import { useAwsAccounts, useVerifyAccount } from "@/lib/api/awsAccounts";
import { cn, formatDate } from "@/lib/utils";

const SECTIONS = [
  { value: "tenant", label: "Tenant", icon: Building2 },
  { value: "users", label: "Users & roles", icon: Users },
  { value: "accounts", label: "AWS accounts", icon: CloudCog },
  { value: "notifications", label: "Notifications", icon: Bell },
  { value: "specs", label: "Spec history", icon: History },
] as const;

export default function SettingsPage() {
  const [section, setSection] = React.useState<(typeof SECTIONS)[number]["value"]>("tenant");

  return (
    <div>
      <PageHeader
        title="Settings"
        description="Tenant configuration, access, connected AWS accounts, notifications and specification history."
        actions={
          <Tabs value={section} onValueChange={(v) => setSection(v as typeof section)}>
            <TabsList>
              {SECTIONS.map((s) => (
                <TabsTrigger key={s.value} value={s.value}><s.icon className="mr-1 h-3.5 w-3.5" />{s.label}</TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        }
      />
      {section === "tenant" && <TenantSection />}
      {section === "users" && <UsersSection />}
      {section === "accounts" && <AccountsSection />}
      {section === "notifications" && <NotificationsSection />}
      {section === "specs" && <SpecsSection />}
    </div>
  );
}

function TenantSection() {
  const tenant = useTenant();
  return (
    <QueryBoundary isLoading={tenant.isLoading} isError={tenant.isError} error={tenant.error} data={tenant.data} onRetry={() => tenant.refetch()}>
      {(t) => (
        <Card className="max-w-xl">
          <CardHeader>
            <CardTitle>{t.name}</CardTitle>
            <CardDescription>{t.slug} · tenant id {t.id}</CardDescription>
          </CardHeader>
          <CardContent className="grid grid-cols-2 gap-3 text-sm">
            <Field label="Plan"><Badge variant="secondary" className="capitalize">{t.plan}</Badge></Field>
            <Field label="State"><Badge variant={t.state === "active" ? "success" : "destructive"} className="capitalize">{t.state}</Badge></Field>
            <Field label="Active spec version"><span>v{t.active_spec_version}</span></Field>
            <Field label="Active policy"><span className="font-mono text-xs">{t.active_policy_id}</span></Field>
            {t.demo && <Field label="Mode"><Badge variant="outline">Demo tenant</Badge></Field>}
          </CardContent>
        </Card>
      )}
    </QueryBoundary>
  );
}

function UsersSection() {
  const users = useUsers();
  const invite = useInviteUser();
  const [email, setEmail] = React.useState("");

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader><CardTitle className="text-sm">Invite a user</CardTitle></CardHeader>
        <CardContent className="flex items-center gap-2">
          <Input placeholder="name@company.com" value={email} onChange={(e) => setEmail(e.target.value)} className="max-w-xs" />
          <Button
            size="sm"
            disabled={!email.includes("@") || invite.isPending}
            onClick={() => invite.mutate({ email, roles: ["viewer"] }, { onSuccess: () => setEmail("") })}
          >
            <UserPlus className="h-3.5 w-3.5" /> Invite as viewer
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle>Users</CardTitle><CardDescription>Roles are additive and scoped to this tenant.</CardDescription></CardHeader>
        <CardContent>
          <QueryBoundary isLoading={users.isLoading} isError={users.isError} error={users.error} data={users.data} onRetry={() => users.refetch()}>
            {(list) => (
              <ul className="divide-y divide-border">
                {list.map((u) => (
                  <li key={u.id} className="flex items-center justify-between gap-3 py-2.5 text-sm">
                    <div className="min-w-0">
                      <p className="truncate font-medium">{u.name}</p>
                      <p className="truncate text-xs text-muted-foreground">{u.email}</p>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      {u.memberships?.[0]?.roles?.map((r) => <Badge key={r} variant="secondary" className="text-[10px] capitalize">{r.replace(/_/g, " ")}</Badge>)}
                      {u.disabled && <Badge variant="destructive">Disabled</Badge>}
                      <span className="w-28 text-right text-[11px] text-muted-foreground">{u.last_login_at ? `seen ${formatDate(u.last_login_at)}` : "never logged in"}</span>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </QueryBoundary>
        </CardContent>
      </Card>
    </div>
  );
}

function AccountsSection() {
  const accounts = useAwsAccounts();
  const verify = useVerifyAccount();
  return (
    <QueryBoundary isLoading={accounts.isLoading} isError={accounts.isError} error={accounts.error} data={accounts.data} onRetry={() => accounts.refetch()}>
      {(list) => (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {list.map((a) => (
            <Card key={a.id}>
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <div>
                  <CardTitle className="text-sm">{a.alias}</CardTitle>
                  <CardDescription>{a.accountId} · {a.environment.replace(/_/g, " ")}{a.isPayer ? " · payer" : ""}</CardDescription>
                </div>
                <Badge variant={a.state === "connected" ? "success" : a.state === "degraded" ? "warning" : "muted"} className="capitalize">{a.state}</Badge>
              </CardHeader>
              <CardContent className="space-y-2 text-xs">
                <p className="text-muted-foreground">Regions: {a.regions.join(", ")}</p>
                <div className="flex flex-wrap gap-1">
                  {a.grantedScopes.map((s) => <Badge key={s} variant="secondary" className="text-[10px] capitalize">{s}</Badge>)}
                </div>
                {a.missingActions.length > 0 && (
                  <div className="rounded-md border border-warning/30 bg-warning/5 p-2">
                    <p className="font-medium text-warning">{a.missingActions.length} missing IAM actions</p>
                    <ul className="mt-0.5 space-y-0.5 font-mono text-[10px] text-muted-foreground">
                      {a.missingActions.map((m) => <li key={m}>{m}</li>)}
                    </ul>
                  </div>
                )}
                <p className="text-muted-foreground">Connected {formatDate(a.connectedAt)} · last verified {formatDate(a.lastVerifiedAt)}</p>
                <Button size="sm" variant="outline" onClick={() => verify.mutate(a.id)} disabled={verify.isPending}>
                  <ShieldCheck className="h-3.5 w-3.5" /> Re-verify
                </Button>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </QueryBoundary>
  );
}

function NotificationsSection() {
  const channels = useNotificationChannels();
  const [overrides, setOverrides] = React.useState<Record<string, boolean>>({});
  return (
    <Card>
      <CardHeader><CardTitle>Notification channels</CardTitle></CardHeader>
      <CardContent>
        <QueryBoundary isLoading={channels.isLoading} isError={channels.isError} error={channels.error} data={channels.data} onRetry={() => channels.refetch()}>
          {(list) => (
            <ul className="divide-y divide-border">
              {list.map((c) => (
                <li key={c.id} className="flex items-center justify-between gap-3 py-2.5 text-sm">
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <Badge variant="secondary" className="uppercase">{c.kind}</Badge>
                      <span className="truncate font-medium">{c.label}</span>
                    </div>
                    <p className="truncate text-xs text-muted-foreground">{c.target}</p>
                    <div className="mt-1 flex flex-wrap gap-1">
                      {c.events.map((e) => <Badge key={e} variant="outline" className="text-[9px]">{e.replace(/_/g, " ")}</Badge>)}
                    </div>
                  </div>
                  <Switch checked={overrides[c.id] ?? c.enabled} onCheckedChange={(v) => setOverrides((o) => ({ ...o, [c.id]: v }))} />
                </li>
              ))}
            </ul>
          )}
        </QueryBoundary>
      </CardContent>
    </Card>
  );
}

function SpecsSection() {
  const versions = useSpecVersions();
  return (
    <QueryBoundary isLoading={versions.isLoading} isError={versions.isError} error={versions.error} data={versions.data} onRetry={() => versions.refetch()}>
      {(list) => (
        <div className="space-y-2">
          {[...list].sort((a, b) => (b.version ?? 0) - (a.version ?? 0)).map((v) => (
            <Card key={v.id}>
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <CardTitle className="text-sm">Version {v.version}</CardTitle>
                <Badge variant={v.status === "approved" ? "success" : "muted"} className="capitalize">{v.status}</Badge>
              </CardHeader>
              {v.diff && v.diff.length > 0 && (
                <CardContent>
                  <ul className="space-y-1.5 text-xs">
                    {v.diff.map((d, i) => (
                      <li key={i} className="rounded-md border border-border p-2">
                        <p className="font-mono font-medium">{d.path}</p>
                        <p className="mt-0.5 text-muted-foreground">
                          <span className={cn(d.kind === "removed" && "text-destructive line-through")}>{d.before ?? "—"}</span>
                          {" → "}
                          <span className={cn(d.kind === "added" && "text-success")}>{d.after ?? "—"}</span>
                        </p>
                        {d.impact && <p className="mt-0.5 text-[11px] text-muted-foreground">{d.impact}</p>}
                      </li>
                    ))}
                  </ul>
                </CardContent>
              )}
            </Card>
          ))}
        </div>
      )}
    </QueryBoundary>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <p className="text-[10px] uppercase text-muted-foreground">{label}</p>
      {children}
    </div>
  );
}
