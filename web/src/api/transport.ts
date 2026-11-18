/**
 * NOTICE: Adapted from kagent-dev/kagent ui/src/api/transport.ts, Apache-2.0.
 *
 * How the app reaches the Workbench server. Every service, kagent's and ours, is
 * gRPC-Web at `<apiBaseUrl>/<package>.<Service>/<Method>`.
 *
 * The interceptors wrap whichever inner transport is in force rather than being
 * passed to `createGrpcWebTransport`, so that the mock fixture transport
 * (`setApiTransport`) runs through exactly the same chain as the live one: the
 * bearer token, the A2A version header and the share token are applied in both.
 */
import { createClient, createContextValues } from "@connectrpc/connect";
import type {
  Client,
  ContextValues,
  Interceptor,
  StreamRequest,
  StreamResponse,
  Transport,
  UnaryRequest,
  UnaryResponse,
} from "@connectrpc/connect";
import { createGrpcWebTransport } from "@connectrpc/connect-web";
import { create } from "@bufbuild/protobuf";
import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  DescService,
  MessageInitShape,
} from "@bufbuild/protobuf";
import { A2AService } from "@/gen/a2a_pb";
import { isStartingError, markStarting } from "./starting";

const REQUEST_TIMEOUT_MS = 30_000;

export type AuthTokenSource = () => string | undefined;
let tokenSource: AuthTokenSource | undefined;
let baseUrl = "/api";

export function registerAuthTokenSource(source: AuthTokenSource | undefined): void {
  tokenSource = source;
}

export function setApiBaseUrl(url: string): void {
  baseUrl = url.replace(/\/$/, "");
  resetApiTransport();
}

/** The bearer token travels in the `authorization` metadata of every call. */
const authInterceptor: Interceptor = (next) => (request) => {
  const token = tokenSource?.();
  if (token) request.header.set("authorization", `Bearer ${token}`);
  return next(request);
};

/** Generated A2A v1 clients do not negotiate the protocol version themselves. */
const a2aVersionInterceptor: Interceptor = (next) => (request) => {
  if (request.service.typeName === A2AService.typeName) request.header.set("A2A-Version", "1.0");
  return next(request);
};

/** Notices the "Workbench is starting" answer of a paused cluster (api/starting.ts). */
const startingInterceptor: Interceptor = (next) => async (request) => {
  try {
    return await next(request);
  } catch (err) {
    if (isStartingError(err)) markStarting();
    throw err;
  }
};

const INTERCEPTORS: Interceptor[] = [startingInterceptor, authInterceptor, a2aVersionInterceptor];

type CallFn = (request: UnaryRequest | StreamRequest) => Promise<UnaryResponse | StreamResponse>;

function chain(inner: CallFn): CallFn {
  return INTERCEPTORS.reduceRight<CallFn>((next, i) => i(next as never) as CallFn, inner);
}

function withInterceptors(inner: Transport): Transport {
  return {
    async unary<I extends DescMessage, O extends DescMessage>(
      method: DescMethodUnary<I, O>,
      signal: AbortSignal | undefined,
      timeoutMs: number | undefined,
      header: HeadersInit | undefined,
      input: MessageInitShape<I>,
      contextValues?: ContextValues,
    ): Promise<UnaryResponse<I, O>> {
      const call = chain(async (request) => {
        if (request.stream) throw new Error("A unary call cannot become a stream.");
        return inner.unary(
          request.method as DescMethodUnary<I, O>,
          request.signal,
          timeoutMs ?? REQUEST_TIMEOUT_MS,
          request.header,
          request.message as MessageInitShape<I>,
          request.contextValues,
        );
      });
      const request: UnaryRequest<I, O> = {
        stream: false,
        service: method.parent,
        method,
        requestMethod: "POST",
        url: `${baseUrl}/${method.parent.typeName}/${method.name}`,
        header: new Headers(header),
        contextValues: contextValues ?? createContextValues(),
        signal: signal ?? new AbortController().signal,
        message: create(method.input, input),
      };
      return (await call(request)) as UnaryResponse<I, O>;
    },
    async stream<I extends DescMessage, O extends DescMessage>(
      method: DescMethodStreaming<I, O>,
      signal: AbortSignal | undefined,
      timeoutMs: number | undefined,
      header: HeadersInit | undefined,
      input: AsyncIterable<MessageInitShape<I>>,
      contextValues?: ContextValues,
    ): Promise<StreamResponse<I, O>> {
      const call = chain(async (request) => {
        if (!request.stream) throw new Error("A stream cannot become a unary call.");
        return inner.stream(
          request.method as DescMethodStreaming<I, O>,
          request.signal,
          timeoutMs,
          request.header,
          request.message as AsyncIterable<MessageInitShape<I>>,
          request.contextValues,
        );
      });
      const request: StreamRequest<I, O> = {
        stream: true,
        service: method.parent,
        method,
        requestMethod: "POST",
        url: `${baseUrl}/${method.parent.typeName}/${method.name}`,
        header: new Headers(header),
        contextValues: contextValues ?? createContextValues(),
        signal: signal ?? new AbortController().signal,
        message: (async function* () {
          for await (const m of input) yield create(method.input, m);
        })(),
      };
      return (await call(request)) as StreamResponse<I, O>;
    },
  };
}

let inner: Transport | undefined;
let live: Transport | undefined;
const clients = new Map<string, unknown>();

function liveTransport(): Transport {
  live ??= createGrpcWebTransport({ baseUrl });
  return live;
}

export function apiTransport(): Transport {
  return withInterceptors(inner ?? liveTransport());
}

export function resetApiTransport(): void {
  live = undefined;
  clients.clear();
}

/** Serve every later call from `transport` (fixtures, tests). `undefined` restores the network. */
export function setApiTransport(transport: Transport | undefined): void {
  resetApiTransport();
  inner = transport;
}

export function serviceClient<T extends DescService>(service: T): Client<T> {
  const existing = clients.get(service.typeName);
  if (existing) return existing as Client<T>;
  const client = createClient(service, apiTransport());
  clients.set(service.typeName, client);
  return client;
}
