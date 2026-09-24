import { useState, type FormEvent } from "react";
import { api, type CreateIncident, type Incident, type ResourceType } from "../api";

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

const empty: CreateIncident = { title: "", description: "", cluster: "local", namespace: "", resource_type: "deployment", resource_name: "" };

export default function CreateIncidentForm({ onCreated }: { onCreated: (inc: Incident, scenario: string) => void }) {
  const [form, setForm] = useState<CreateIncident>(empty);
  const [preset, setPreset] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

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
        <div className="flex gap-1.5" role="group" aria-label="Sample incidents">
          {Object.entries(PRESETS).map(([key, p]) => (
            <button
              key={key}
              type="button"
              onClick={() => {
                setForm(p.incident);
                setPreset(key);
              }}
              className="rounded-full border border-zinc-200 px-2.5 py-0.5 text-xs hover:border-indigo-500 dark:border-zinc-700"
            >
              {p.label}
            </button>
          ))}
        </div>
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
            <input className="field font-mono" value={form.cluster} onChange={set("cluster")} spellCheck={false} autoComplete="off" />
          </label>
          <label className="label">
            Namespace
            <input className="field font-mono" value={form.namespace} onChange={set("namespace")} placeholder="payments" spellCheck={false} autoComplete="off" />
          </label>
          <label className="label">
            Kind
            <select className="field" value={form.resource_type} onChange={(e) => setForm({ ...form, resource_type: e.target.value as ResourceType })}>
              <option value="deployment">Deployment</option>
              <option value="service">Service</option>
              <option value="pod">Pod</option>
            </select>
          </label>
          <label className="label">
            Name
            <input className="field font-mono" value={form.resource_name} onChange={set("resource_name")} placeholder="payment-service" spellCheck={false} autoComplete="off" />
          </label>
        </div>
        {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        <button type="submit" className="btn-primary" disabled={busy}>
          {busy ? "Creating…" : "Create incident"}
        </button>
      </form>
    </div>
  );
}
