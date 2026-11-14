/** One typed client per service. Every screen calls the server through these. */
import { serviceClient } from "./transport";
import { A2AService } from "@/gen/a2a_pb";
import { SessionService } from "@/gen/kagent/api/v1/sessions_pb";
import { CheckpointService } from "@/gen/kagent/api/v1/checkpoints_pb";
import { ScheduledRunService } from "@/gen/kagent/api/v1/scheduled_runs_pb";
import { AgentService } from "@/gen/kagent/api/v1/agents_pb";
import { IdentityService } from "@/gen/workbench/v1/identity_pb";
import { ApprovalService } from "@/gen/workbench/v1/approvals_pb";
import { AppService } from "@/gen/workbench/v1/apps_pb";
import { AuditService } from "@/gen/workbench/v1/audit_pb";
import { NotificationService } from "@/gen/workbench/v1/notifications_pb";
import { UsageService } from "@/gen/workbench/v1/usage_pb";
import { ComponentService } from "@/gen/workbench/v1/components_pb";

export const api = {
  get a2a() { return serviceClient(A2AService); },
  get sessions() { return serviceClient(SessionService); },
  get checkpoints() { return serviceClient(CheckpointService); },
  get schedules() { return serviceClient(ScheduledRunService); },
  get agents() { return serviceClient(AgentService); },
  get identity() { return serviceClient(IdentityService); },
  get approvals() { return serviceClient(ApprovalService); },
  get apps() { return serviceClient(AppService); },
  get audit() { return serviceClient(AuditService); },
  get notifications() { return serviceClient(NotificationService); },
  get usage() { return serviceClient(UsageService); },
  get components() { return serviceClient(ComponentService); },
};
