import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate, useBlocker } from "react-router-dom";
import * as Tabs from "@radix-ui/react-tabs";
import * as Dialog from "@radix-ui/react-dialog";
import {
  mdiDocker,
  mdiPlus,
  mdiCodeBraces,
  mdiRefresh,
  mdiPlay,
  mdiStop,
  mdiRestart,
  mdiDeleteOutline,
  mdiTextBoxOutline,
  mdiInformationOutline,
  mdiChartBoxOutline,
  mdiCogOutline,
  mdiFolderOutline,
  mdiDownload,
  mdiOpenInNew,
  mdiLoading,
  mdiCheckCircleOutline,
  mdiAlertCircleOutline,
} from "@mdi/js";
import {
  Button,
  Icon,
  Notice,
  DialogContent,
  FolderPicker,
  bytes,
} from "@panasms/ui";
import { request } from "@panasms/client";
import { registerModule } from "@panasms/runtime";
import { registerTranslations, translator } from "@panasms/i18n";
import en from "./locales/en.json";
import ru from "./locales/ru.json";
import uk from "./locales/uk.json";
import "./containers.css";
registerTranslations("containers", { en, ru, uk });
const tr = translator("containers");
const api = <T,>(path: string, body?: unknown) =>
  request<T>("module-api/containers/" + path, body ? "POST" : "GET", body);
type Container = {
  Id: string;
  Names: string[];
  Image: string;
  State: string;
  Status: string;
  Labels: Record<string, string>;
  Ports: {
    IP: string;
    PrivatePort: number;
    PublicPort: number;
    Type: string;
  }[];
};
type PlatformCheck = { host: {os:string;architecture:string;variant?:string}; platforms: {os:string;architecture:string;variant?:string}[]; status: string };
type State = {
  engine: {
    engine: string;
    compose: string;
    compatible: boolean;
    reachable: boolean;
    problem: string;
    missing: string[];
    canInstall: boolean;
    canStart: boolean;
    dataRoot: string;
  };
  containers: Container[];
  projects: { name: string }[];
  images: { Id: string; RepoTags: string[]; Size: number; platformCheck?: PlatformCheck }[];
  networks: { Id: string; Name: string; Driver: string }[];
  volumes: { Name: string; Driver: string }[];
  migration: null | { old: string; new: string; phase: string };
};
type Job = {
  displayName?: string;
  id: string;
  action: string;
  target: string;
  status: string;
  stage: string;
  error?: string;
  created: string;
};
type Action = { action: string; target?: string; [key: string]: unknown };
const stateKey = ["containers", "state"];
const jobsKey = ["containers", "jobs"];
function useStateData() {
  return useQuery({
    queryKey: stateKey,
    queryFn: () => api<State>("state"),
    staleTime: 10000,
    retry: 1,
  });
}
function useJobs() {
  return useQuery({
    queryKey: jobsKey,
    queryFn: () => api<Job[]>("jobs"),
    staleTime: 5000,
    retry: 1,
  });
}
const phaseKeys: Record<string, string> = {
  Preparing: "preparing",
  "Installing Docker components": "phase.packages",
  "Checking package changes": "phase.check",
  "Validating Compose": "phase.compose",
  "Downloading images and starting services": "phase.deploy",
  "Downloading image and inspecting exposed ports": "phase.pull",
  "Stopping containers for storage transfer": "phase.stop",
  "Copying Docker data; original files are preserved": "phase.copy",
  "Starting Docker at the new location": "phase.start",
};
function actionLabel(action: string) {
  const key: Record<string, string> = {
    setup: "install",
    "engine.start": "engineStart",
    "storage.move": "move",
    "storage.recover": "recover",
    "project.save": "addCompose",
    "image.create": "addImage",
    "image.pull": "pull",
  };
  return tr(key[action] || action.split(".").at(-1) || action);
}
function jobName(job: Job) {
  const name = job.displayName || job.target;
  if (name && !/^(sha256:)?[a-f0-9]{12,64}$/.test(name)) return name;
  const kind = job.action === "image.create" ? "container" : job.action.split(".")[0];
  return tr("object." + (['image', 'container', 'project', 'network', 'volume'].includes(kind) ? kind : 'docker'));
}
function completionMessage(job: Job) {
  const kind = job.action === "image.create" ? "container" : job.action.split(".")[0];
  const action = job.action.split(".")[1];
  const object = tr("object." + (['image', 'container', 'project', 'network', 'volume'].includes(kind) ? kind : 'docker'));
  const label = jobName(job);
  const subject = label === object ? object : object + " «" + label + "»";
  if (job.status !== "succeeded") return `${tr("failed")} · ${actionLabel(job.action)} · ${subject}`;
  if (job.action === "image.create") return tr("containerStarted", { name: label });
  const result = ['remove','start','stop','restart','pull','save','create'].includes(action) ? tr("result." + action) : tr("done");
  return `${subject}: ${result}`;
}
const phaseLabel = (job: Job) =>
  phaseKeys[job.stage] ? tr(phaseKeys[job.stage]) : actionLabel(job.action);
const problem = (st: State["engine"]) =>
  st.problem.startsWith("Missing components:")
    ? tr("missing", { packages: st.missing.join(", ") })
    : st.problem;
const toast = (text: string) =>
  window.dispatchEvent(new CustomEvent("panasms:toast", { detail: text }));
function operationID() {
  const v = new Uint8Array(16);
  crypto.getRandomValues(v);
  return Array.from(v, (x) => x.toString(16).padStart(2, "0")).join("");
}
function ActionIcon({
  icon,
  label,
  onClick,
  disabled = false,
}: {
  icon: string;
  label: string;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <Button title={label} onClick={onClick} disabled={disabled}>
      <Icon path={icon} />
    </Button>
  );
}
function Background() {
  const q = useQueryClient();
  const jobs = useJobs();
  const previous = useRef<Record<string, string> | null>(null);
  useEffect(() => {
    let ws: WebSocket | undefined;
    let timer: ReturnType<typeof setTimeout>;
    let stopped = false;
    const refresh = () => {
      void q.invalidateQueries({ queryKey: jobsKey });
      void q.invalidateQueries({ queryKey: stateKey });
    };
    const connect = () => {
      ws = new WebSocket(
        `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/api/v1/module-api/containers/events`,
      );
      ws.onopen = refresh;
      ws.onmessage = refresh;
      ws.onclose = () => {
        if (!stopped) timer = setTimeout(connect, 3000);
      };
    };
    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      ws?.close();
    };
  }, [q]);
  useEffect(() => {
    if (!jobs.data) return;
    if (previous.current)
      for (const j of jobs.data) {
        if (previous.current[j.id] === "running" && j.status !== "running")
          toast(
            completionMessage(j),
          );
      }
    previous.current = Object.fromEntries(
      jobs.data.map((j) => [j.id, j.status]),
    );
  }, [jobs.data]);
  const active = jobs.data?.find((j) => j.status === "running");
  if (!active) return null;
  return (
    <Link
      className="ongoing-task"
      to="/containers/tasks"
      title={`${tr("title")} · ${jobName(active)} · ${phaseLabel(active)}`}
      aria-label={`${tr("title")} · ${phaseLabel(active)}`}
    >
      <Icon path={mdiLoading} />
    </Link>
  );
}
function Tasks() {
  const data = useJobs();
  if (data.isPending) return <p>{tr("loading")}</p>;
  return (
    <div className="containers-jobs">
      {data.error && <Notice error>{data.error.message}</Notice>}
      {[...(data.data ?? [])].reverse().map((j) => (
        <article key={j.id}>
          <div>
            <Icon
              path={
                j.status === "succeeded"
                  ? mdiCheckCircleOutline
                  : j.status === "running"
                    ? mdiLoading
                    : mdiAlertCircleOutline
              }
            />
            <strong>{jobName(j)}</strong>
            <span>{tr(j.status)}</span>
          </div>
          <small>
            {actionLabel(j.action)} · {new Date(j.created).toLocaleString()}
          </small>
          {j.status === "running" && <p role="status">{phaseLabel(j)}</p>}
          {j.error && <p className="error-text">{j.error}</p>}
        </article>
      ))}
    </div>
  );
}
function TaskContribution() {
  const jobs = useJobs();
  if (!jobs.data?.length) return null;
  return (
    <section className="containers-module">
      <h3>{tr("title")}</h3>
      <Tasks />
    </section>
  );
}
function Modal({
  title,
  children,
  footer,
  close,
  busy = false,
  dirty = false,
  compact = false,
}: {
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  close: () => void;
  busy?: boolean;
  dirty?: boolean;
  compact?: boolean;
}) {
  return (
    <Dialog.Root
      open
      onOpenChange={(v) => {
        if (!v) close();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <DialogContent
          className="settings-dialog containers-dialog"
          aria-describedby={undefined}
          variant={compact ? "compact" : "form"}
          intent={compact ? "confirm" : "edit"}
          dirty={dirty}
          busy={busy}
          message={tr("submitting")}
          header={<Dialog.Title>{title}</Dialog.Title>}
          footer={footer ? <div className="dialog-actions">{footer}</div> : undefined}
        >
          {children}
        </DialogContent>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
function Confirmation({
  a,
  name,
  onClose,
}: {
  a: Action;
  name: string;
  onClose: () => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const q = useQueryClient();
  const id = useRef(operationID());
  async function submit() {
    setBusy(true);
    setError("");
    try {
      await api("action", { ...a, displayName: name, id: id.current });
      void q.invalidateQueries({ queryKey: jobsKey });
      toast(tr("accepted"));
      onClose();
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
    } finally {
      setBusy(false);
    }
  }
  const isRemove = a.action.endsWith(".remove");
  const moving = a.action === "storage.move";
  const recovering = a.action === "storage.recover";
  return (
    <Modal
      compact
      title={tr(
        moving
          ? "move"
          : recovering
            ? "recover"
            : isRemove
              ? "confirmRemove"
              : "confirmStop",
      )}
      busy={busy}
      close={onClose}
      footer={
        <>
          <Button data-dialog-cancel onClick={onClose}>
            {tr("cancel")}
          </Button>
          <Button
            className={isRemove ? "danger" : "primary"}
            onClick={() => void submit()}
          >
            {tr(
              moving
                ? "move"
                : recovering
                  ? "recover"
                  : isRemove
                    ? "remove"
                    : "apply",
            )}
          </Button>
        </>
      }
    >
      <p>
        <strong>{name}</strong>
      </p>
      <p>
        {tr(
          moving
            ? "moveHint"
            : recovering
              ? "recoverHint"
              : isRemove
                ? a.action === "volume.remove"
                  ? "volumeWarning"
                  : a.action === "container.remove"
                    ? "containerWarning"
                    : a.action === "project.remove"
                      ? "applicationWarning"
                      : "resourceWarning"
                : "stopHint",
        )}
      </p>
      {error && <Notice error>{error}</Notice>}
    </Modal>
  );
}
function Inspector({
  id,
  name,
  mode,
  onClose,
}: {
  id: string;
  name: string;
  mode: string;
  onClose: () => void;
}) {
  const data = useQuery({
    queryKey: ["containers", mode, id],
    queryFn: () => api<any>(mode + "?id=" + encodeURIComponent(id)),
  });
  return (
    <Modal
      title={`${tr(mode === "inspect" ? "details" : mode)} · ${name}`}
      close={onClose}
    >
      {data.isPending ? (
        <p>{tr("loading")}</p>
      ) : data.error ? (
        <Notice error>{data.error.message}</Notice>
      ) : (
        <pre className="containers-output">
          {mode === "logs"
            ? data.data.text || tr("noLogs")
            : JSON.stringify(data.data, null, 2)}
        </pre>
      )}
    </Modal>
  );
}
function PlatformStatus({ check }: { check?: PlatformCheck }) {
  const status = check?.status || "unknown";
  const label = tr("platform."+status);
  const platforms = check?.platforms.map(p=>[p.os,p.architecture,p.variant].filter(Boolean).join("/")).join(", ") || tr("platform.unknown");
  const host = check?.host ? [check.host.os,check.host.architecture,check.host.variant].filter(Boolean).join("/") : "";
  return <span className={"image-platform " + status} title={`${label}${host ? " · NAS: "+host : ""}`}>
    <Icon path={status === "compatible" ? mdiCheckCircleOutline : status === "incompatible" ? mdiAlertCircleOutline : mdiInformationOutline} size={18} />
    <span>{platforms}<small>{label}</small></span>
  </span>;
}
function RemotePlatform({ image }: { image: string }) {
  const [reference,setReference] = useState("");
  useEffect(()=>{ setReference(""); const timer=setTimeout(()=>setReference(image),500);return()=>clearTimeout(timer); },[image]);
  const check = useQuery({queryKey:["containers","platform",reference],queryFn:()=>api<PlatformCheck>("images/platform?image="+encodeURIComponent(reference)),enabled:!!reference && reference === image,staleTime:60000,retry:false});
  return <div className="image-platform-check" role="status">
    {!reference || reference !== image || check.isFetching ? tr("platform.checking") : <PlatformStatus check={check.data} />}
    {check.isError && <Button onClick={()=>void check.refetch()}>{tr("refresh")}</Button>}
  </div>;
}
function ImageTags({ repository, tag, onChange }: { repository: string; tag: string; onChange: (tag: string) => void }) {
  const hub = /^(?:docker\.io\/)?(?:[a-z0-9][a-z0-9_-]*\/)?[a-z0-9][a-z0-9_.-]*$/.test(repository);
  const [manual, setManual] = useState(false);
  const tags = useInfiniteQuery({
    queryKey: ["containers", "image-tags", repository],
    queryFn: ({ pageParam }) => api<{tags:string[];more:boolean}>("images/tags?repository="+encodeURIComponent(repository)+"&page="+pageParam),
    initialPageParam: 1,
    getNextPageParam: (last, pages) => last.more ? pages.length + 1 : undefined,
    enabled: hub,
    staleTime: 60000,
    retry: false,
  });
  const options = [...new Set([tag, ...(tags.data?.pages.flatMap(p => p.tags) ?? [])])].filter(Boolean);
  return <div className="container-tag-picker">
    <label className="field">{tr("imageTag")}
      {hub && !manual && !tags.isError ? <select value={tag} onChange={(e) => e.target.value === "__manual" ? setManual(true) : onChange(e.target.value)}>
        {options.map(t => <option key={t} value={t}>{t}</option>)}
        <option value="__manual">{tr("tagManual")}</option>
      </select> : <input value={tag} onChange={(e) => onChange(e.target.value)} placeholder="8.4" maxLength={128} />}
      <small>{tr("tagHint")}</small>
    </label>
    {tags.isFetching && <p className="muted" role="status">{tr("tagsLoading")}</p>}
    {hub && tags.isError && <p role="status">{tr("tagsError")}</p>}
    {hub && tags.hasNextPage && <Button disabled={tags.isFetchingNextPage} onClick={() => void tags.fetchNextPage()}>{tr("tagsMore")}</Button>}
    <p className="container-image-reference"><span className="muted">{tr("imageSelected")}</span> <strong>{repository}:{tag}</strong></p>
  </div>;
}
function ImagePicker({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const [term, setTerm] = useState("");
  const [selected, setSelected] = useState("");
  const input = value.trim();
  const colon = input.lastIndexOf(":");
  const hasTag = !input.includes("@") && colon > input.lastIndexOf("/");
  const repository = hasTag ? input.slice(0,colon) : input;
  const tag = hasTag ? input.slice(colon+1) : "latest";
  const searchable = input.length >= 2 && input.length <= 100 && /^[a-zA-Z0-9][a-zA-Z0-9_/-]*$/.test(input) && input !== selected;
  useEffect(() => {
    setTerm("");
    if (!searchable) return;
    const timer = setTimeout(() => setTerm(input), 400);
    return () => clearTimeout(timer);
  }, [input, searchable]);
  const results = useQuery({
    queryKey: ["containers", "image-search", term],
    queryFn: () => api<{ name: string; description: string; is_official: boolean; star_count: number }[]>("images/search?term=" + encodeURIComponent(term)),
    enabled: searchable && term === input,
    staleTime: 60000,
    retry: false,
  });
  const visible = searchable && term === input;
  return <div className="container-image-picker">
    <label className="field">{tr("image")}
      <input value={value} onChange={(e) => { setSelected(""); onChange(e.target.value); }} placeholder="nginx, postgres, ghcr.io/owner/image:tag" autoComplete="off" maxLength={255} aria-describedby="container-image-hint" />
      <small id="container-image-hint">{tr("imageSearchHint")}</small>
    </label>
    {(selected || hasTag || (input.includes(".") && input.includes("/"))) && !input.includes("@") && <ImageTags key={repository} repository={repository} tag={tag} onChange={(next) => { setSelected(repository); onChange(repository + ":" + next); }} />}
    {input && (selected || hasTag || input.includes("@") || (input.includes(".") && input.includes("/"))) && !input.endsWith(":") && <RemotePlatform image={input} />}
    {searchable && (!visible || results.isFetching) && <p role="status" className="muted">{tr("imageSearching")}</p>}
    {visible && results.isError && <div role="status"><p>{tr("imageSearchError")}</p><Button onClick={() => void results.refetch()}>{tr("refresh")}</Button></div>}
    {visible && results.isSuccess && !results.isFetching && <>
      <p className="muted" role="status">{tr(results.data.length ? "imageSearchResults" : "imageSearchEmpty")}</p>
      <ul className="container-image-results" aria-label={tr("imageSearchResults")}>
        {results.data.map((item) => <li key={item.name}><button type="button" onClick={() => { setSelected(item.name); onChange(item.name + ":latest"); }}>
          <span className="container-image-result-title"><strong>{item.name}</strong>{item.is_official && <small>{tr("imageOfficial")}</small>}<small>★ {item.star_count}</small></span>
          {item.description && <span className="muted">{item.description}</span>}
        </button></li>)}
      </ul>
    </>}
  </div>;
}
type PortDraft = {host:string;container:number;published:number;protocol:string};
type MountDraft = {source:string;target:string;readOnly:boolean};
type LocalConfig = {id:string; platformCheck:PlatformCheck; ports:PortDraft[];environment:Record<string,string>;volumes:string[];addresses:string[]};
type EditorSeed = {image:string; ports:PortDraft[]; environment:Record<string,string>; mounts:MountDraft[]; webPort:number; network:string};
function LocalImageEditor({onChange,initial}:{onChange:(value:Action|null)=>void;initial?:EditorSeed}) {
  const state=useStateData();
  const [selected,setSelected]=useState(initial?.image ?? "");
  const [config,setConfig]=useState<LocalConfig|null>(null);
  const [ports,setPorts]=useState<PortDraft[]>([]);
  const [env,setEnv]=useState<{key:string;value:string}[]>([]);
  const [mounts,setMounts]=useState<MountDraft[]>([]);
  const [web,setWeb]=useState(0);
  const [network,setNetwork]=useState("");
  const [folder,setFolder]=useState<number|null>(null);
  const detail=useQuery({queryKey:["containers","image-config",selected],queryFn:()=>api<LocalConfig>("images/config?image="+encodeURIComponent(selected)),enabled:!!selected,retry:false,staleTime:Infinity,refetchOnWindowFocus:false});
  useEffect(()=>{
    if (!detail.data || detail.data.id!==selected) return;
    setConfig(detail.data);setPorts(initial?.ports ?? detail.data.ports);
    setEnv(Object.entries(initial?.environment ?? detail.data.environment).map(([key,value])=>({key,value})));
    setMounts(initial?.mounts ?? detail.data.volumes.map(target=>({target,source:"",readOnly:false})));setWeb(initial?.webPort ?? 0);setNetwork(initial?.network ?? "");
  },[selected,detail.data]);
  const valid=!!config && config.id===selected && config.platformCheck.status==="compatible" &&
    ports.every(p=>p.container>=1&&p.container<=65535&&p.published>=1&&p.published<=65535) &&
    env.every(e=>!!e.key&&!e.key.includes("=")) && new Set(env.map(e=>e.key)).size===env.length &&
    mounts.every(m=>!m.source||(m.target.startsWith("/")&&m.target!=="/")) &&
    new Set(mounts.filter(m=>m.source).map(m=>m.target)).size===mounts.filter(m=>m.source).length;
  useEffect(()=>{
    onChange(valid&&folder===null?{action:"image.create",image:selected,ports,environment:Object.fromEntries(env.map(e=>[e.key,e.value])),mounts:mounts.filter(m=>m.source),webPort:ports.some(p=>p.protocol==="tcp"&&p.published===web)?web:0,network}:null);
  },[valid,selected,ports,env,mounts,web,network,folder]);
  function port(index:number,patch:Partial<PortDraft>){setPorts(rows=>rows.map((row,i)=>i===index?{...row,...patch}:row))}
  function mount(index:number,patch:Partial<MountDraft>){setMounts(rows=>rows.map((row,i)=>i===index?{...row,...patch}:row))}
  if(folder!==null)return <div><Button onClick={()=>setFolder(null)}>{tr("cancel")}</Button><FolderPicker policy="share" initialPath={mounts[folder].source} onChoose={source=>{mount(folder,{source});setFolder(null)}}/></div>;
  return <>
    <label className="field">{tr("localImage")}<select disabled={!!initial} value={selected} onChange={e=>{setConfig(null);setSelected(e.target.value)}}><option value="">{tr("selectImage")}</option>{state.data?.images.map(image=><option key={image.Id} value={image.Id} disabled={image.platformCheck?.status==="incompatible"}>{image.RepoTags?.filter(t=>t!=="<none>:<none>").join(", ")||tr("untagged")} · {image.platformCheck?.platforms.map(p=>p.os+"/"+p.architecture).join(", ")||tr("platform.unknown")}{image.platformCheck?.status==="incompatible"?" · "+tr("platform.incompatible"):""}</option>)}</select><small>{tr("localImageHint")}</small></label>
    {state.error&&<Notice error>{state.error.message}</Notice>}
    {detail.isFetching&&<p role="status">{tr("loading")}</p>}
    {detail.error&&<Notice error>{detail.error.message}</Notice>}
    {config&&config.id===selected&&<>
      <PlatformStatus check={config.platformCheck}/>

      <section className="containers-editor"><div className="containers-editor-heading"><h3>{tr("ports")}</h3><ActionIcon icon={mdiPlus} label={tr("addPort")} onClick={()=>setPorts([...ports,{host:"0.0.0.0",container:80,published:8080,protocol:"tcp"}])}/></div>
        {!ports.length&&<p className="muted">{tr("noPorts")}</p>}
        {ports.length>0&&<div className="containers-port-row containers-column-head" aria-hidden="true">{["containerPort","hostPort","protocol","bindAddress"].map(k=><span key={k}>{tr(k)}</span>)}<span/></div>}
        {ports.map((p,i)=><div className="containers-port-row" key={i}>
          <label className="field"><span className="containers-row-label">{tr("containerPort")}</span><input type="text" inputMode="numeric" pattern="[0-9]*" maxLength={5} value={p.container || ""} onChange={e=>{if(/^[0-9]{0,5}$/.test(e.target.value))port(i,{container:Number(e.target.value)})}}/></label>
          <label className="field"><span className="containers-row-label">{tr("hostPort")}</span><input type="text" inputMode="numeric" pattern="[0-9]*" maxLength={5} value={p.published || ""} onChange={e=>{if(/^[0-9]{0,5}$/.test(e.target.value))port(i,{published:Number(e.target.value)})}}/></label>
          <label className="field"><span className="containers-row-label">{tr("protocol")}</span><select value={p.protocol} onChange={e=>port(i,{protocol:e.target.value})}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
          <label className="field"><span className="containers-row-label">{tr("bindAddress")}</span><select value={p.host} onChange={e=>port(i,{host:e.target.value})}>{config.addresses.map(a=><option key={a} value={a}>{a==="0.0.0.0"?tr("allInterfaces")+" · IPv4":a==="::"?tr("allInterfaces")+" · IPv6":a}</option>)}</select></label>
          <ActionIcon icon={mdiDeleteOutline} label={tr("remove")} onClick={()=>setPorts(ports.filter((_,n)=>n!==i))}/>
        </div>)}
        <label className="field">{tr("webPort")}<select value={ports.some(p=>p.protocol==="tcp"&&p.published===web)?web:0} onChange={e=>setWeb(Number(e.target.value))}><option value={0}>{tr("none")}</option>{Array.from(new Set(ports.filter(p=>p.protocol==="tcp").map(p=>p.published))).map(p=><option key={p} value={p}>{p}</option>)}</select><small>{tr("webHint")}</small></label>
      </section>

      <section className="containers-editor"><div className="containers-editor-heading"><h3>{tr("volumesTitle")}</h3><ActionIcon icon={mdiPlus} label={tr("addMount")} onClick={()=>setMounts([...mounts,{source:"",target:"/data",readOnly:false}])}/></div><p className="muted">{tr("anonymousVolumeHint")}</p>
        {mounts.length>0&&<div className="containers-mount-row containers-column-head" aria-hidden="true">{["containerPath","hostFolder","accessMode"].map(k=><span key={k}>{tr(k)}</span>)}<span/></div>}
        {mounts.map((m,i)=><div className="containers-mount-row" key={i}><label className="field"><span className="containers-row-label">{tr("containerPath")}</span><input value={m.target} onChange={e=>mount(i,{target:e.target.value})}/></label><div className="field"><span className="containers-row-label">{tr("hostFolder")}</span><div className="containers-location"><span>{m.source||tr("unmapped")}</span><ActionIcon icon={mdiFolderOutline} label={tr("choose")} onClick={()=>setFolder(i)}/>{m.source&&<ActionIcon icon={mdiDeleteOutline} label={tr("unmap")} onClick={()=>mount(i,{source:""})}/>}</div></div><label className="field"><span className="containers-row-label">{tr("accessMode")}</span><select disabled={!m.source} value={m.readOnly?"ro":"rw"} onChange={e=>mount(i,{readOnly:e.target.value==="ro"})}><option value="rw">{tr("readWrite")}</option><option value="ro">{tr("readOnly")}</option></select></label><ActionIcon icon={mdiDeleteOutline} label={tr("remove")} onClick={()=>setMounts(mounts.filter((_,n)=>n!==i))}/></div>)}
      </section>
<details className="containers-editor containers-env-details"><summary>{tr("environmentTitle")} <span className="muted">· {env.length}</span></summary><div className="containers-editor-heading"><ActionIcon icon={mdiPlus} label={tr("addVariable")} onClick={()=>setEnv([...env,{key:"",value:""}])}/></div>
        {env.length>0&&<div className="containers-env-row containers-column-head" aria-hidden="true"><span>{tr("variableName")}</span><span>{tr("value")}</span><span/></div>}
        {env.map((entry,i)=><div className="containers-env-row" key={i}><label className="field"><span className="containers-row-label">{tr("variableName")}</span><input value={entry.key} onChange={e=>setEnv(env.map((r,n)=>n===i?{...r,key:e.target.value}:r))}/></label><label className="field"><span className="containers-row-label">{tr("value")}</span><input type={/password|secret|token/i.test(entry.key)?"password":"text"} autoComplete="off" value={entry.value} onChange={e=>setEnv(env.map((r,n)=>n===i?{...r,value:e.target.value}:r))}/></label><ActionIcon icon={mdiDeleteOutline} label={tr("remove")} onClick={()=>setEnv(env.filter((_,n)=>n!==i))}/></div>)}
      </details>
      <label className="field">{tr("network")}<select value={network} onChange={e=>setNetwork(e.target.value)}><option value="">{tr("defaultNetwork")}</option>{state.data?.networks.filter(n=>n.Driver==="bridge"&&n.Name!=="bridge").map(n=><option key={n.Id}>{n.Name}</option>)}</select></label>
      {!valid&&<Notice error>{tr("invalidConfiguration")}</Notice>}
    </>}
  </>;
}

function Create({
  kind,
  name = "",
  onClose,
}: {
  kind: string;
  name?: string;
  onClose: () => void;
}) {
  const [target, setTarget] = useState(name);
  const [image, setImage] = useState("");
  const [compose, setCompose] = useState("");
  const [variables, setVariables] = useState("");
  const [imageAction, setImageAction] = useState<Action|null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const q = useQueryClient();
  const state = useStateData();
  const id = useRef(operationID());
  const saved = useQuery({
    queryKey: ["containers", "project", name],
    queryFn: () =>
      api<{ compose: string }>("project?id=" + encodeURIComponent(name)),
    enabled: !!name && kind === "compose",
  });
  const dirty = !!(
    target !== name || image || compose !== (saved.data?.compose ?? "") ||
    variables || imageAction
  );
  useEffect(() => {
    if (saved.data) setCompose(saved.data.compose);
  }, [saved.data]);
  async function submit() {
    setError("");
    let a: Action = { action: kind + ".create", target };
    if (kind === "compose")
      a = { action: "project.save", target, compose, env: variables };
    if (kind === "image") { if (!imageAction) return; a = {...imageAction, target}; }
    if (kind === "pull") a = { action: "image.pull", target: image, image };
    setBusy(true);
    try {
      await api("action", { ...a, displayName: kind === "pull" ? image : target, id: id.current });
      void q.invalidateQueries({ queryKey: jobsKey });
      toast(tr("accepted"));
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={
        name
          ? `${tr("edit")} · ${name}`
          : tr(
              kind === "compose"
                ? "addCompose"
                : kind === "image"
                  ? "addImage"
                  : kind === "pull"
                    ? "pull"
                    : "create",
            )
      }
      close={onClose}
      busy={busy}
      dirty={dirty}
      footer={
          <>
            <Button data-dialog-cancel onClick={onClose}>
              {tr("cancel")}
            </Button>
            <Button
              className="primary"
              onClick={() => void submit()}
              disabled={(kind !== "pull" && !/^[a-z][a-z0-9_-]{1,47}$/.test(target)) || busy || !!saved.error || saved.isFetching || (kind === "pull" && (!image.trim() || image.endsWith(":"))) || (kind === "image" && !imageAction)}
            >
              {tr(kind === "pull" ? "pull" : "apply")}
            </Button>
          </>
      }
    >
      {error && <Notice error>{error}</Notice>}
      {saved.error && <Notice error>{saved.error.message}</Notice>}
        <div className="containers-form">
          {kind !== "pull" && (
            <label className="field">
              {tr("name")}
              <input
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                disabled={!!name}
                maxLength={48}
                placeholder="my-app"
                autoFocus
              />
            </label>
          )}
          {kind === "pull" && <ImagePicker value={image} onChange={setImage} />}
          {kind === "image" && <LocalImageEditor onChange={setImageAction} />}
          {kind === "pull" && <p className="muted">{tr("imagePullHint")}</p>}
          {kind === "compose" && (
            <>
              <label className="field">
                {tr("file")}
                <input
                  type="file"
                  accept=".yml,.yaml,.json"
                  onChange={async (e) => {
                    const f = e.target.files?.[0];
                    if (f) {
                      if (f.size > 256 * 1024) {
                        setError("Compose file exceeds 256 KiB");
                        return;
                      }
                      setCompose(await f.text());
                    }
                  }}
                />
              </label>
              <label className="field">
                {tr("compose")}
                <textarea
                  rows={12}
                  value={compose}
                  onChange={(e) => setCompose(e.target.value)}
                  spellCheck={false}
                />
                <small>{tr("composeHint")}</small>
              </label>
            </>
          )}
          {kind === "compose" && (
            <label className="field">
              {tr("env")}
              <textarea
                rows={3}
                value={variables}
                onChange={(e) => setVariables(e.target.value)}
                spellCheck={false}
              />
            </label>
          )}
        </div>
    </Modal>
  );
}
export function DockerSettings() {
  const state = useStateData();
  const [root, setRoot] = useState("");
  const [picker, setPicker] = useState(false);
  const [confirm, setConfirm] = useState<Action | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const q = useQueryClient();
  const jobs = useJobs();
  const working = jobs.data?.some((j) => j.status === "running");
  async function install() {
    setBusy(true);
    setError("");
    try {
      await api("action", {
        id: operationID(),
        action: "setup",
        root,
        target: "Docker",
      });
      void q.invalidateQueries({ queryKey: jobsKey });
      toast(tr("accepted"));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }
  const st = state.data?.engine;
  return (
    <div className="containers-module containers-settings">
      <h2>{tr("settings")}</h2>
      {state.isPending && <p>{tr("loading")}</p>}
      {state.error && <Notice error>{state.error.message}</Notice>}
      {error && <Notice error>{error}</Notice>}
      {st && (
        <>
          <dl className="containers-facts">
            <dt>Docker Engine</dt>
            <dd>{st.engine || "—"}</dd>
            <dt>Compose</dt>
            <dd>{st.compose || "—"}</dd>
            <dt>{tr("status")}</dt>
            <dd>{tr(st.compatible ? "ready" : "unavailable")}</dd>
          </dl>
          {st.problem && <Notice>{problem(st)}</Notice>}
          <section>
            <h3>{tr("dataRoot")}</h3>
            <p className="muted">{tr("dataHint")}</p>
            <div className="containers-location">
              <strong>{root || st.dataRoot || "—"}</strong>
              <ActionIcon
                icon={mdiFolderOutline}
                label={tr("choose")}
                onClick={() => setPicker(true)}
                disabled={working}
              />
            </div>
            {st.canInstall ? (
              <>
                <p>{tr("installHint")}</p>
                <p>{st.missing.join(", ")}</p>
                <Button
                  className="primary"
                  onClick={() => void install()}
                  disabled={working || busy || (!st.dataRoot && !root)}
                >
                  {tr("install")}
                </Button>
              </>
            ) : st.dataRoot && root && root !== st.dataRoot ? (
              <Button
                onClick={() =>
                  setConfirm({ action: "storage.move", root, target: root })
                }
                disabled={working}
              >
                {tr("move")}
              </Button>
            ) : null}
          </section>
          {state.data?.migration && (
            <>
              <Notice error>{tr("recoverHint")}</Notice>
              <Button
                onClick={() => setConfirm({ action: "storage.recover" })}
                disabled={working}
              >
                {tr("recover")}
              </Button>
            </>
          )}
        </>
      )}
      {picker && (
        <Modal title={tr("newFolder")} close={() => setPicker(false)}>
          <FolderPicker
            policy="home"
            hint={tr("dataHint")}
            newFolder
            defaultName="docker"
            onChoose={(v) => {
              setRoot(v);
              setPicker(false);
            }}
          />
        </Modal>
      )}
      {confirm && (
        <Confirmation
          a={confirm}
          name={root || tr("dataRoot")}
          onClose={() => setConfirm(null)}
        />
      )}
    </div>
  );
}
function ContainerPage({id,view,controls}:{id:string;view:string;controls:(type:string,id:string,name:string,running:boolean,anyRunning?:boolean)=>ReactNode}) {
  const state=useStateData();
  const navigate=useNavigate();
  const identity=useRef<{project:string;service:string}|null>(null);
  const container=state.data?.containers.find(c=>c.Id===id);
  useEffect(()=>{
    if(container) {identity.current={project:container.Labels["com.docker.compose.project"],service:container.Labels["com.docker.compose.service"]};return}
    const previous=identity.current;
    if(previous?.project&&previous.service) {
      const replacement=state.data?.containers.find(c=>c.Labels["com.docker.compose.project"]===previous.project&&c.Labels["com.docker.compose.service"]===previous.service);
      if(replacement)navigate(`/containers/containers/${replacement.Id}/${view}`,{replace:true});
    }
  },[container,state.data,view,navigate]);
  const name=container?.Names[0]?.replace(/^\//,"") ?? id.slice(0,12);
  const detail=useQuery({queryKey:["containers","inspect",id],queryFn:()=>api<any>("inspect?id="+encodeURIComponent(id)),refetchInterval:5000});
  const logs=useQuery({queryKey:["containers","logs",id],queryFn:()=>api<{text:string}>("logs?id="+encodeURIComponent(id)),enabled:view==="logs",refetchInterval:view==="logs"?4000:false});
  const stats=useQuery({queryKey:["containers","stats",id],queryFn:()=>api<any>("stats?id="+encodeURIComponent(id)),enabled:["details","stats"].includes(view)&&container?.State==="running",refetchInterval:["details","stats"].includes(view)?3000:false});
  const [samples,setSamples]=useState<number[]>([]);
  const previous=useRef<{cpu:number;system:number}|null>(null);
  useEffect(()=>{
    const data=stats.data;if(!data)return;
    const cpu=data.cpu_stats?.cpu_usage?.total_usage??0,system=data.cpu_stats?.system_cpu_usage??0;
    const prev=previous.current;
    if(prev&&system>prev.system&&cpu>=prev.cpu){const value=(cpu-prev.cpu)/(system-prev.system)*(data.cpu_stats?.online_cpus||1)*100;setSamples(values=>[...values.slice(-39),value])}
    previous.current={cpu,system};
  },[stats.data]);
  const project=container?.Labels["com.docker.compose.project"];
  const managed=!!project&&!!state.data?.projects.some(p=>p.name===project);
  const single=managed&&state.data?.containers.filter(c=>c.Labels["com.docker.compose.project"]===project).length===1;
  const tabs=["details","logs",...(managed?["configuration"]:[])];
  const active=tabs.includes(view)?view:"details";
  useEffect(()=>{if(view==="stats")navigate(`/containers/containers/${id}/details`,{replace:true})},[view,id,navigate]);
  const local=state.data?.images.find(i=>i.Id===container?.Image);
  const image=local?.RepoTags?.join(", ")||container?.Image.replace(/^sha256:/,"").slice(0,12)||"—";
  const data=detail.data;
  const memory=stats.data?.memory_stats;
  const used=Math.max(0,(memory?.usage??0)-(memory?.stats?.inactive_file??0));
  const limit=memory?.limit??0;
  const cpu=samples.at(-1);
  const net=Object.values(stats.data?.networks??{}) as {rx_bytes:number;tx_bytes:number}[];
  const io=(stats.data?.blkio_stats?.io_service_bytes_recursive??[]) as {op:string;value:number}[];
  return <div className="containers-module container-detail-page">
    <Link to="/containers/containers" className="container-back">← {tr("containers")}</Link>
    <div className="page-heading"><div><h2 className="container-detail-title">{name}</h2><p className="muted">{image} · {container?tr(container.State):tr("loading")}</p></div>{container&&controls(single?"project":"container",single?project!:id,name,container.State==="running",["running","restarting"].includes(container.State))}</div>
    <nav className="tabs container-inner-tabs" aria-label={tr("details")}>{tabs.map(tab=><Link key={tab} aria-current={active===tab?"page":undefined} data-state={active===tab?"active":"inactive"} to={`/containers/containers/${id}/${tab}`}>{tr(tab)}</Link>)}</nav>
    <div className="container-detail-body">
    {active==="details"&&(detail.isPending?<p>{tr("loading")}</p>:detail.error?<Notice error>{detail.error.message}</Notice>:data&&<>
      <dl className="container-facts">{[
        ["status",tr(data.State?.Status??"unknown")],["containerID",id.slice(0,12)],["createdAt",new Date(data.Created).toLocaleString()],["restartCount",String(data.RestartCount??0)],["restartPolicy",data.HostConfig?.RestartPolicy?.Name||"—"]
      ].map(([key,value])=><div key={key}><dt>{tr(key)}</dt><dd>{value}</dd></div>)}</dl>
      {data.State?.Error&&<Notice error>{data.State.Error}</Notice>}
    {(container?.State!=="running"?<p className="muted">{tr("statsStopped")}</p>:stats.error?<Notice error>{stats.error.message}</Notice>:!stats.data?<p>{tr("loading")}</p>:<>
      <div className="container-metrics"><section><h2>CPU</h2><strong>{cpu===undefined?"—":cpu.toFixed(1)+"%"}</strong><small>{tr("cpuHint")}</small><svg role="img" aria-label={tr("cpuChart")} viewBox="0 0 400 90"><path d="M0 89H400" stroke="var(--line)"/>{samples.length>1&&<polyline fill="none" stroke="var(--accent)" strokeWidth="2" points={samples.map((v,i)=>`${i*400/(samples.length-1)},${88-v/Math.max(100,...samples)*84}`).join(" ")}/>}</svg></section>
      <section><h2>{tr("memory")}</h2><strong>{memory?.usage===undefined?tr("unavailable"):bytes(used)}</strong>{limit>0?<><small>{tr("memoryLimit")} {bytes(limit)}</small><meter min={0} max={limit} value={used}/></>:<small>{tr("memoryUnavailable")}</small>}</section>
      <section><h2>{tr("networkTransfer")}</h2><strong>↓ {bytes(net.reduce((sum,n)=>sum+n.rx_bytes,0))}</strong><span>↑ {bytes(net.reduce((sum,n)=>sum+n.tx_bytes,0))}</span><small>{tr("totalSinceStart")}</small></section>
      <section><h2>{tr("diskTransfer")}</h2><strong>↓ {bytes(io.filter(i=>i.op.toLowerCase()==="read").reduce((s,i)=>s+i.value,0))}</strong><span>↑ {bytes(io.filter(i=>i.op.toLowerCase()==="write").reduce((s,i)=>s+i.value,0))}</span><small>{tr("readWriteTotals")}</small></section></div>
    </>)}
      <h2>{tr("ports")}</h2><div className="container-connections">{Object.entries(data.NetworkSettings?.Ports??{}).map(([port,bindings])=><div className="container-connection" key={port}><strong>{port}</strong><span>←</span><span>{(bindings as {HostIp:string;HostPort:string}[]|null)?.map(b=>b.HostIp+":"+b.HostPort).join(", ")||tr("unmapped")}</span></div>)}</div>
      <h2>{tr("networks")}</h2><div className="container-connections">{Object.entries(data.NetworkSettings?.Networks??{}).map(([network,info])=><div className="container-connection" key={network}><Icon path={mdiDocker}/><strong>{network}</strong><span>{(info as {IPAddress:string}).IPAddress||"—"}</span></div>)}</div>
      <h2>{tr("volumesTitle")}</h2><div className="container-connections">{(data.Mounts??[]).map((m:any)=><div className="container-connection" key={m.Destination}><Icon path={mdiFolderOutline}/><span>{m.Type==="bind"?m.Source:tr("dockerVolume")}</span><span>→</span><strong>{m.Destination}</strong><small>{tr(m.RW?"readWrite":"readOnly")}</small></div>)}</div>
      {container?.Ports.filter(p=>p.Type==="tcp"&&String(p.PublicPort)===container.Labels["com.panasms.web-port"]).map(p=><a key={p.PublicPort} className="button" href={`http://${location.hostname}:${p.PublicPort}/`} target="_blank" rel="noopener noreferrer">{tr("open")}</a>)}
    </>)}
    {active==="logs"&&(logs.error?<Notice error>{logs.error.message}</Notice>:<pre className="container-log" tabIndex={0}>{logs.data?.text||tr(logs.isPending?"loading":"empty")}</pre>)}
    {active==="configuration"&&container&&<ContainerConfiguration key={id} container={container}/>}
    </div>

  </div>;
}
function ContainerConfiguration({container}:{container:Container}) {
  const project=container.Labels["com.docker.compose.project"];
  const service=container.Labels["com.docker.compose.service"];
  const saved=useQuery({queryKey:["containers","project-form",project],queryFn:()=>api<{compose:string}>("project?id="+encodeURIComponent(project)),staleTime:Infinity});
  const [draft,setDraft]=useState<Action|null>(null);
  const baseline=useRef<string|null>(null);
  const acceptDraft=(value:Action|null)=>{if(value&&baseline.current===null)baseline.current=JSON.stringify(value);setDraft(value)};
  const dirty=baseline.current!==null&&JSON.stringify(draft)!==baseline.current;
  const blocker=useBlocker(dirty);
  useEffect(()=>{
    if(!dirty)return;
    const handler=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue=""};
    window.addEventListener("beforeunload",handler);return()=>window.removeEventListener("beforeunload",handler);
  },[dirty]);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const jobs=useJobs();
  const q=useQueryClient();
  if(saved.isPending)return <p>{tr("loading")}</p>;
  if(saved.error)return <Notice error>{saved.error.message}</Notice>;
  const document=JSON.parse(saved.data!.compose);
  const config=document.services?.[service];
  if(!config)return <Notice error>{tr("configurationUnavailable")}</Notice>;
  const initial:EditorSeed={
    image:container.Image,
    ports:(config.ports??[]).map((p:any)=>({host:p.host_ip||"0.0.0.0",container:Number(p.target),published:Number(p.published),protocol:p.protocol||"tcp"})),
    environment:config.environment??{},
    mounts:(config.volumes??[]).map((m:any)=>({source:m.source??"",target:m.target,readOnly:!!m.read_only})),
    webPort:Number(config.labels?.["com.panasms.web-port"])||0,
    network:document.networks?.["panasms-"+service]?.name??document.networks?.shared?.name??""
  };
  async function save(){
    if(!draft)return;
    setBusy(true);setError("");
    try {
      const next=JSON.parse(saved.data!.compose),s=next.services[service];
      s.ports=(draft.ports as PortDraft[]).map(p=>({target:p.container,published:String(p.published),protocol:p.protocol,host_ip:p.host}));
      s.environment=draft.environment;
      const edited=draft.mounts as MountDraft[];
      s.volumes=edited.map(m=>({...config.volumes?.find((v:any)=>v.target===m.target),type:m.source.startsWith("/")?"bind":"volume",source:m.source,target:m.target,read_only:m.readOnly}));
      s.labels={...s.labels};delete s.labels["com.panasms.web-port"];if(draft.webPort)s.labels["com.panasms.web-port"]=String(draft.webPort);
      if(draft.network!==initial.network) {
        if(!Array.isArray(s.networks)&&s.networks) {delete s.networks.shared;delete s.networks["panasms-"+service];}
        if(draft.network) {const key="panasms-"+service;s.networks={...s.networks,[key]:{}};next.networks={...next.networks,[key]:{external:true,name:draft.network}}}
        if(!s.networks||!Object.keys(s.networks).length){s.networks={default:{}};next.networks={...next.networks,default:{}}}
      }
      await api("action",{action:"project.save",target:project,displayName:project,compose:JSON.stringify(next).replace(/\$/g,"$$$$"),id:operationID()});
      baseline.current=JSON.stringify(draft);toast(tr("accepted"));void q.invalidateQueries({queryKey:jobsKey});
    }catch(e){setError(e instanceof Error?e.message:String(e))}finally{setBusy(false)}
  }
  return <div className="containers-form container-config-form">
    <p className="muted">{tr("configureHint")}</p>
    <LocalImageEditor initial={initial} onChange={acceptDraft}/>
    {error&&<Notice error>{error}</Notice>}
    {blocker.state==="blocked"&&<Modal compact title={tr("discardTitle")} close={()=>blocker.reset()} footer={<><Button onClick={()=>blocker.reset()}>{tr("cancel")}</Button><Button onClick={()=>blocker.proceed()}>{tr("discard")}</Button></>}><p>{tr("discardHint")}</p></Modal>}
    <Button className="primary" disabled={!draft||busy||jobs.data?.some(j=>j.status==="running"||j.status==="queued")} onClick={()=>void save()}>{tr(busy?"submitting":"apply")}</Button>
  </div>;
}

function Page() {
  const state = useStateData();
  const jobs = useJobs();
  const q = useQueryClient();
  const navigate = useNavigate();
  const loc = useLocation();
  const requestedTab = loc.pathname.split("/")[2];
  const tab = !requestedTab || requestedTab === "apps" ? "containers" : requestedTab;
  useEffect(() => {
    if (!requestedTab || requestedTab === "apps") navigate("/containers/containers", { replace: true });
  }, [requestedTab, navigate]);
  const [create, setCreate] = useState<{ kind: string; name?: string } | null>(
    null,
  );
  const [confirm, setConfirm] = useState<{ a: Action; name: string } | null>(
    null,
  );
  const [inspect, setInspect] = useState<{
    id: string;
    name: string;
    mode: string;
  } | null>(null);
  const [error, setError] = useState("");
  const working = jobs.data?.some((j) => j.status === "running");
  const st = state.data?.engine;
  const ready = !!st?.compatible && !state.data?.migration && !working;
  async function run(a: Action, name: string) {
    if (
      a.action.endsWith(".remove") ||
      a.action.endsWith(".stop") ||
      a.action.endsWith(".restart")
    ) {
      setConfirm({ a, name });
      return;
    }
    setError("");
    try {
      await api("action", { ...a, displayName: name, id: operationID() });
      void q.invalidateQueries({ queryKey: jobsKey });
      toast(tr("accepted"));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }
  const controls = (
    type: string,
    id: string,
    name: string,
    running: boolean,
    anyRunning = running,
  ) => (
    <div className="containers-actions">
      {[
        ["start", mdiPlay],
        ["stop", mdiStop],
        ["restart", mdiRestart],
        ["remove", mdiDeleteOutline],
      ].map(([a, icon]) => (
        <ActionIcon
          key={a}
          icon={icon}
          label={`${tr(a)} · ${name}`}
          disabled={
            !ready ||
            (a === "start" && running) ||
            (a === "stop" && !anyRunning) ||
            (a === "restart" && !anyRunning) ||
            (type === "container" && a === "remove" && running)
          }
          onClick={() => void run({ action: `${type}.${a}`, target: id }, name)}
        />
      ))}
    </div>
  );
  const allProjects = new Set([
    ...(state.data?.projects.map((p) => p.name) ?? []),
    ...(state.data?.containers
      .map((c) => c.Labels["com.docker.compose.project"])
      .filter(Boolean) ?? []),
  ]);
  const containerRow = (c: NonNullable<typeof state.data>["containers"][number]) => {
    const name = c.Names[0]?.replace(/^\//, "") || c.Id.slice(0, 12);
    const project = c.Labels["com.docker.compose.project"];
    const single = project && state.data?.containers.filter((item) => item.Labels["com.docker.compose.project"] === project).length === 1;
    const managedSingle = single && state.data?.projects.some((item) => item.name === project);
    const localImage = state.data?.images.find(image => image.Id === c.Image || image.Id.replace(/^sha256:/, "") === c.Image);
    const imageLabel = localImage?.RepoTags?.filter(tag => tag !== "<none>:<none>").join(", ") ||
      (c.Image.startsWith("sha256:") ? c.Image.slice(7, 19) : c.Image);
    return <tr key={c.Id}>
      <th scope="row"><Link className="container-row-name" to={`/containers/containers/${c.Id}/details`} title={c.Id.slice(0, 12)}><Icon path={mdiDocker} size={20} />{name}</Link></th>
      <td aria-label={tr("image")} title={c.Image}>{imageLabel}</td>
      <td aria-label={tr("status")}><span className={"container-row-state containers-status " + c.State} title={c.Status}>
        <Icon path={c.State === "running" ? mdiCheckCircleOutline : mdiStop} size={18} />{tr(c.State)}
      </span></td>
      <td className="container-row-actions">
                    <div className="containers-actions">
                    {controls(managedSingle ? "project" : "container", managedSingle ? project : c.Id, name, c.State === "running")}
                    </div>

      </td>
    </tr>;
  };
  const detailID = loc.pathname.split("/")[3];

  const standalone = state.data?.containers.filter((c) => !c.Labels["com.docker.compose.project"]) ?? [];
  return (
    <div className="containers-module">
      <div className="page-heading">
        <div>
          <h1>{tr("title")}</h1>
          <p className="muted">{tr("preview")}</p>
        </div>
        <div className="containers-actions">
          <ActionIcon
            icon={mdiRefresh}
            label={tr("refresh")}
            onClick={() => {
              void q.invalidateQueries({ queryKey: ["containers"] });
            }}
          />
          <Link
            className="button icon-only"
            data-tooltip={tr("settings")}
            aria-label={tr("settings")}
            to="/settings/containers/general"
          >
            <Icon path={mdiCogOutline} />
          </Link>
          {tab === "containers" && !detailID && (
            <>
              <ActionIcon
                icon={mdiPlus}
                label={tr("addImage")}
                disabled={!ready}
                onClick={() => setCreate({ kind: "image" })}
              />

            </>
          )}
          {["networks", "volumes", "images"].includes(tab) && (
            <ActionIcon
              icon={tab === "images" ? mdiDownload : mdiPlus}
              label={tr(tab === "images" ? "pull" : "create")}
              disabled={!ready}
              onClick={() =>
                setCreate({
                  kind: tab === "images" ? "pull" : tab.slice(0, -1),
                })
              }
            />
          )}
        </div>
      </div>
      {error && <Notice error>{error}</Notice>}
      {state.error && (
        <Notice error>
          {state.data ? tr("lastKnown") + " " : ""}
          {state.error.message}
        </Notice>
      )}
      {st && !st.compatible && (
        <div className="containers-engine">
          <Icon path={mdiAlertCircleOutline} />
          <div>
            <strong>{tr("unavailable")}</strong>
            <p>{problem(st)}</p>
          </div>
          <Link className="button" to="/settings/containers/general">
            {tr("settings")}
          </Link>
          {st.canStart && (
            <ActionIcon
              icon={mdiPlay}
              label={tr("engineStart")}
              onClick={() => void run({ action: "engine.start" }, "Docker")}
              disabled={working}
            />
          )}
        </div>
      )}
      {state.data?.migration && (
        <Notice error>
          <Link to="/settings/containers/general">{tr("recoverHint")}</Link>
        </Notice>
      )}
      <Tabs.Root value={tab} onValueChange={(value) => navigate("/containers/" + value)} activationMode="manual" orientation="vertical" className="settings-layout settings-page">
        <Tabs.List className="settings-nav" aria-label={tr("title")}>
          {["containers", "images", "networks", "volumes", "tasks"].map(
            (t) => (
              <Tabs.Trigger key={t} value={t}>
                {tr(t)}
              </Tabs.Trigger>
            ),
          )}
        </Tabs.List>
        <Tabs.Content key={tab} value={tab} className="settings-content">
          {state.isPending ? (
            <p role="status">{tr("loading")}</p>
          ) : tab === "containers" && detailID ? (
            <ContainerPage key={detailID} id={detailID} view={loc.pathname.split("/")[4] || "details"} controls={controls}/>
          ) : tab === "tasks" ? (
            <Tasks />
          ) : tab === "containers" ? (
            <div className="container-collection">
              {[...allProjects].sort().map((name) => {
                const members = state.data?.containers.filter((c) => c.Labels["com.docker.compose.project"] === name) ?? [];
                const owned = state.data?.projects.some((p) => p.name === name);
                if (members.length === 1) return <div className="containers-table-wrap" key={name}><table aria-label={name}><tbody>{members.map(containerRow)}</tbody></table></div>;
                return <section className="container-project-group" key={name} aria-label={name}>
                  <header className="container-project-bar">
                    <div className="container-project-title"><Icon path={mdiCodeBraces} size={20} /><strong>{name}</strong><span className="muted">{tr(owned ? "owned" : "external")}</span></div>
                    {owned && <div className="containers-actions">
                      <ActionIcon icon={mdiCodeBraces} label={`${tr("edit")} · ${name}`} disabled={!ready} onClick={() => setCreate({ kind: "compose", name })} />
                      {controls("project", name, name, members.length > 0 && members.every((c) => c.State === "running"), members.some((c) => c.State === "running"))}
                    </div>}
                  </header>
                  <div className="container-project-members containers-table-wrap"><table aria-label={name}><tbody>{members.map(containerRow)}{!members.length && <tr><td>{tr("empty")}</td></tr>}</tbody></table></div>
                </section>;
              })}
              {standalone.length > 0 && <div className="containers-table-wrap"><table aria-label={tr("containers")}><tbody>{standalone.map(containerRow)}</tbody></table></div>}
              {!allProjects.size && !standalone.length && <p>{tr("empty")}</p>}
            </div>
          ) : (
            <div className="containers-table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{tr("name")}</th>
                    <th>{tr(tab === "images" ? "size" : "driver")}</th>
                    {tab === "images" && <th>{tr("platform.column")}</th>}
                    <th>{tr("actions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {(tab === "images"
                    ? state.data?.images.map((i) => ({
                        id: i.Id,
                        name: i.RepoTags?.join(", ") || i.Id.slice(0, 19),
                        detail: bytes(i.Size),
                        platformCheck: i.platformCheck,
                      }))
                    : tab === "networks"
                      ? state.data?.networks.map((i) => ({
                          id: i.Id,
                          name: i.Name,
                          detail: i.Driver,
                          platformCheck: undefined as PlatformCheck | undefined,
                        }))
                      : state.data?.volumes.map((i) => ({
                          id: i.Name,
                          name: i.Name,
                          detail: i.Driver,
                          platformCheck: undefined as PlatformCheck | undefined,
                        }))
                  )?.map((i) => (
                    <tr key={i.id} className={"platformCheck" in i && i.platformCheck?.status === "incompatible" ? "image-incompatible" : undefined}>
                      <td>{i.name}</td>
                      <td>{i.detail}</td>
                      {tab === "images" && <td><PlatformStatus check={"platformCheck" in i ? i.platformCheck : undefined} /></td>}
                      <td>
                        <ActionIcon
                          icon={mdiDeleteOutline}
                          label={`${tr("remove")} · ${i.name}`}
                          disabled={
                            !ready ||
                            (tab === "networks" &&
                              ["bridge", "host", "none"].includes(i.name))
                          }
                          onClick={() =>
                            void run(
                              {
                                action: tab.slice(0, -1) + ".remove",
                                target: i.id,
                              },
                              i.name,
                            )
                          }
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Tabs.Content>
      </Tabs.Root>
      {create && <Create {...create} onClose={() => setCreate(null)} />}{" "}
      {confirm && (
        <Confirmation {...confirm} onClose={() => setConfirm(null)} />
      )}{" "}
      {inspect && <Inspector {...inspect} onClose={() => setInspect(null)} />}
    </div>
  );
}
function ModulePage() {
  if (!Tabs?.Root || !FolderPicker) return <Notice error>{tr("coreUpdateRequired")}</Notice>;
  return <Page/>;
}
registerModule({
  id: "containers",
  title: tr("title"),
  path: "/containers",
  routes: ["apps", "containers", "containers/:id/:view", "containers/:id", "images", "networks", "volumes", "tasks"],
  icon: mdiDocker,
  component: ModulePage,
  backgroundIndicator: Background,
  tasks: TaskContribution,
  settings: [
    {
      id: "containers",
      title: tr("title"),
      icon: mdiDocker,
      component: DockerSettings,
      routes: ["general"],
    },
  ],
});
