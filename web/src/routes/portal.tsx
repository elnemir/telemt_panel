import {createFileRoute} from "@tanstack/react-router";
import {CustomerPortal} from "../customer/CustomerPortal";
export const Route=createFileRoute("/portal")({component:CustomerPortal});
