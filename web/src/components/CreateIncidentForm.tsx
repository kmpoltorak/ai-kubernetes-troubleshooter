import { useEffect, useState, type FormEvent } from "react";
import { Field } from "./Hint";
import { api, type ClusterState, type CreateIncident, type Incident, type ResourceType, type Runtime } from "../api";

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
export default function CreateIncidentForm({ runtime, clusters: states, onCreated }: {
  runtime: Runtime | null; clusters: ClusterState[] | null; onCreated: (inc: Incident, scenario: string) => void;
}) {
  const clusters = runtime?.clusters ?? [];
  const namespacesOf = (name: string) => clusters.find((c) => c.name === name)?.allowed_namespaces ?? [];
  const only = (list: string[]) => (list.length === 1 ? list[0] : "");
  const cluster = only(clusters.map((c) => c.name));
  const empty: CreateIncident = { title: "", description: "", cluster, namespace: only(namespacesOf(cluster)), resource_type: "deployment", resource_name: "" };
  const [form, setForm] = useState<CreateIncident>(empty);
  const allowed = namespacesOf(form.cluster);
  const unreachable = new Set(states?.filter((s) => s.status === "unreachable").map((s) => s.name));
  const [preset, setPreset] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [names, setNames] = useState<{ list: string[]; error: string }>({ list: [], error: "" });

  // Name suggestions come from the cluster; typing a name stays possible when listing fails.
  useEffect(() => {
    setNames({ list: [], error: "" });
    if (!runtime || !form.cluster || !form.namespace) return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      api.targets(form.cluster, form.namespace, form.resource_type, controller.signal).then(
        (list) => setNames({ list, error: "" }),
        (e) => { if (!controller.signal.aborted) setNames({ list: [], error: (e as Error).message }); });
    }, 250);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [runtime, form.cluster, form.namespace, form.resource_type]);

  const kind = kindLabel[form.resource_type] ?? form.resource_type;
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
                setForm({ ...p.incident, cluster: clusters[0]?.name ?? "" });
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
        <Field label={<>Title <span className="font-normal">(optional)</span></>}>
          {(id) => <input id={id} className="field" value={form.title} onChange={set("title")} maxLength={200} placeholder="payment-service pods keep restarting" />}
        </Field>
        <Field label="Description" hint="What users see and since when. It is stored with the incident and given to the analyzer. Known secret patterns are masked, but do not paste secrets.">
          {(id) => <textarea id={id} className="field resize-y" rows={3} value={form.description} onChange={set("description")} maxLength={5000} placeholder="What are users seeing?" />}
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Cluster" hint="The clusters this installation reads (KUBERNETES_CLUSTERS_FILE, or KUBERNETES_CLUSTER_NAME for one). An unreachable cluster can still be chosen; its investigation reports the error.">
            {(id) => clusters.length > 1 ? (
              <select id={id} className="field font-mono" value={form.cluster}
                onChange={(e) => { const c = e.target.value; setForm({ ...form, cluster: c, namespace: only(namespacesOf(c)), resource_name: "" }); }}>
                <option value="">Choose…</option>
                {clusters.map((c) => <option key={c.name} value={c.name}>{c.name}{unreachable.has(c.name) ? " (unreachable)" : ""}</option>)}
              </select>
            ) : (
              <input id={id} className="field cursor-default border-transparent bg-zinc-100 font-mono text-zinc-700 dark:border-transparent dark:bg-zinc-800/60 dark:text-zinc-300"
                value={form.cluster} readOnly />
            )}
          </Field>
          <Field label="Namespace" hint={allowed.length > 0
            ? "Only the namespaces this installation may read in the chosen cluster."
            : "Any namespace the read-only account can read: this installation sets no allowlist."}>
            {(id) => allowed.length > 0 ? (
              <select id={id} className="field font-mono" value={form.namespace} onChange={set("namespace")}>
                {allowed.length > 1 && <option value="">Choose…</option>}
                {allowed.map((ns) => <option key={ns} value={ns}>{ns}</option>)}
              </select>
            ) : (
              <input id={id} className="field font-mono" value={form.namespace} onChange={set("namespace")} placeholder={form.cluster ? "payments" : "Choose a cluster first"} disabled={!form.cluster} spellCheck={false} autoComplete="off" />
            )}
          </Field>
          <Field label="Kind" hint="What the incident is about. A workload (Deployment, StatefulSet, Job…) leads to its pods, an Ingress to its first backend's pods, an autoscaler to the workload it scales. Extra kinds come from KUBERNETES_CUSTOM_RESOURCES.">
            {(id) => <select id={id} className="field" value={form.resource_type} onChange={(e) => setForm({ ...form, resource_type: e.target.value as ResourceType })}>
              {(runtime?.resource_types ?? [form.resource_type]).map((k) => <option key={k} value={k}>{kindLabel[k] ?? k}</option>)}
            </select>}
          </Field>
          <Field label={`${kind} name`} hint={`The name of the ${kind} in the chosen namespace, as kubectl get shows it. Names listed from the cluster are suggested; any name can be typed.`}>
            {(id) => <>
              <input id={id} className="field font-mono" value={form.resource_name} onChange={set("resource_name")} placeholder="payment-service" spellCheck={false} autoComplete="off" list="target-names" />
              <datalist id="target-names">{names.list.map((n) => <option key={n} value={n} />)}</datalist>
            </>}
          </Field>
        </div>
        {form.namespace && (names.error
          ? <p className="text-xs text-amber-800 dark:text-amber-300">Could not list names ({names.error}). Type the name.</p>
          : names.list.length > 0 && <p className="text-xs text-zinc-600 dark:text-zinc-400">{names.list.length} × {kind} in {form.namespace}: pick from {kind} name or type one.</p>)}
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <button type="submit" className="btn-primary" disabled={busy}>
          {busy ? "Creating…" : "Create incident"}
        </button>
      </form>
    </div>
  );
}
