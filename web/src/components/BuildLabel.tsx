import type { BuildInfo } from "../api";

export default function BuildLabel({ build }: { build: BuildInfo | null }) {
  if (!build) return null;
  const description = `Responding replica: version ${build.version}, revision ${build.revision}${build.built_at ? `, built ${build.built_at}` : ", build time unknown"}`;
  return (
    <span className="ml-2 text-xs text-zinc-500" title={description} aria-label={description}>
      {build.version} · {build.revision.slice(0, 12)}
    </span>
  );
}
