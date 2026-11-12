import {createFileRoute} from "@tanstack/react-router";
import {BillingPage} from "../../billing/BillingPage";
export const Route=createFileRoute("/_authed/billing")({component:BillingPage});
