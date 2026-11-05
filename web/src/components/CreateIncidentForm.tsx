import { useEffect, useState, type FormEvent } from "react";
import { api, type CreateIncident, type Incident, type ResourceType, type Runtime } from "../api";

// Samples load a matching simulation scenario for the first investigation.
const PRESETS: Record<string, { label: string; incident: CreateIncident }> = {
  crash_loop_backoff: {
    label: "CrashLoop",
    incident: {
      title: "payment-service pods keep restarting",
      description: "Pods are repeatedly restarting after the 1.4.3 release.",
      cluster: "local", namespace: "payments", resource_type: "deployment", resource_name: "payment-service",
    },
  },
  oom_killed: {
    label: "OOM",
    incident: {
      title: "settlement restarts under load",
      description: "Pods restart during the nightly settlement batch.",
      cluster: "local", namespace: "payments", resource_type: "deployment", resource_name: "settlement",
    },
  },
  service_selector_mismatch: {
    label: "Service",
    incident: {
      title: "checkout unreachable",
      description: "Clients get connection errors calling checkout although pods look healthy.",
      cluster: "local", namespace: "payments", resource_type: "service", resource_name: "checkout",
    },
  },
  network_policy_block: {
    label: "NetPol",
    incident: {
      title: "ledger cannot reach its database",
      description: "Readiness fails since the security team rolled out new policies.",
      cluster: "local", namespace: "payments", resource_type: "deployment", resource_name: "ledger",
    },
  },
};

const kindLabel: Record<string, string> = {
  deployment: "Deployment", statefulset: "StatefulSet", daemonset: "DaemonSet", job: "Job", cronjob: "CronJob", service: "Service", pod: "Pod",
  ingress: "Ingress", persistentvolumeclaim: "PersistentVolumeClaim", horizontalpodautoscaler: "HorizontalPodAutoscaler",
};

// Samples exist only in simulation; live mode never offers them.
export default function CreateIncidentForm({ runtime, onCreated }: { runtime: Runtime | null; onCreated: (inc: Incident, scenario: string) => void }) {
  const cluster = runtime?.cluster_name ?? "";
  const allowed = runtime?.allowed_namespaces ?? [];
  const empty: CreateIncident = { title: "", description: "", cluster, namespace: allowed.length === 1 ? allowed[0] : "", resource_type: "deployment", resource_name: "" };
  const [form, setForm] = useState<CreateIncident>(empty);
  const [preset, setPreset] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [names, setNames] = useState<{ list: string[]; error: string }>({ list: [], error: "" });

  // Name suggestions come from the cluster; typing a name stays possible when listing fails.
  useEffect(() => {
    setNames({ list: [], error: "" });
    if (!runtime || !form.namespace) return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      api.targets(form.namespace, form.resource_type, controller.signal).then(
        (list) => setNames({ list, error: "" }),
        (e) => { if (!controller.signal.aborted) setNames({ list: [], error: (e as Error).message }); });
    }, 250);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [runtime, form.namespace, form.resource_type]);

  const set = (key: keyof CreateIncident) => (e: { target: { value: string } }) => setForm({ ...form, [key]: e.target.value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const inc = await api.createIncident(form);
      setError("");
      setForm(empty);
      onCreated(inc, preset);
      setPreset("");
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">New incident</h2>
        {runtime?.mode === "simulation" && <div className="flex gap-1.5" role="group" aria-label="Sample incidents">
          {Object.entries(PRESETS).map(([key, p]) => (
            <button
              key={key}
              type="button"
              onClick={() => {
                setForm({ ...p.incident, cluster });
                setPreset(key);
              }}
              className="rounded-full border border-zinc-200 px-2.5 py-0.5 text-xs hover:border-indigo-500 dark:border-zinc-700"
            >
              {p.label}
            </button>
          ))}
        </div>}
      </div>
      <form onSubmit={submit} className="flex flex-col gap-3" noValidate>
        <label className="label">
          <span>
            Title <span className="font-normal">(optional)</span>
          </span>
          <input className="field" value={form.title} onChange={set("title")} maxLength={200} placeholder="payment-service pods keep restarting" />
        </label>
        <label className="label">
          Description
          <textarea className="field resize-y" rows={3} value={form.description} onChange={set("description")} maxLength={5000} placeholder="What are users seeing?" />
        </label>
        <div className="grid grid-cols-2 gap-3">
          <label className="label">
            Cluster
            <input className="field cursor-default border-transparent bg-zinc-100 font-mono text-zinc-700 dark:border-transparent dark:bg-zinc-800/60 dark:text-zinc-300"
              value={form.cluster} readOnly title="Set by the installation (KUBERNETES_CLUSTER_NAME)" />
          </label>
          <label className="label">
            Namespace
            {allowed.length > 0 ? (
              <select className="field font-mono" value={form.namespace} onChange={set("namespace")}>
                {allowed.length > 1 && <option value="">Choose…</option>}
                {allowed.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
              </select>
            ) : (
              <input className="field font-mono" value={form.namespace} onChange={set("namespace")} placeholder="payments" spellCheck={false} autoComplete="off" />
            )}
          </label>
          <label className="label">
            Kind
            <select className="field" value={form.resource_type} onChange={(e) => setForm({ ...form, resource_type: e.target.value as ResourceType })}>
              {(runtime?.resource_types ?? [form.resource_type]).map((k) => <option key={k} value={k}>{kindLabel[k] ?? k}</option>)}
            </select>
          </label>
          <label className="label">
            Name
            <input className="field font-mono" value={form.resource_name} onChange={set("resource_name")} placeholder="payment-service" spellCheck={false} autoComplete="off" list="target-names" />
            <datalist id="target-names">{names.list.map((n) => <option key={n} value={n} />)}</datalist>
          </label>
        </div>
        {form.namespace && (names.error
          ? <p className="text-xs text-amber-800 dark:text-amber-300">Could not list names ({names.error}). Type the name.</p>
          : names.list.length > 0 && <p className="text-xs text-zinc-600 dark:text-zinc-400">{names.list.length} × {kindLabel[form.resource_type] ?? form.resource_type} in {form.namespace}: pick from Name or type one.</p>)}
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <button type="submit" className="btn-primary" disabled={busy}>
          {busy ? "Creating…" : "Create incident"}
        </button>
      </form>
    </div>
  );
}
