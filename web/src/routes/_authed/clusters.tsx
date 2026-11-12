import {createFileRoute} from "@tanstack/react-router";
import {ClustersPage} from "../../clusters/ClustersPage";
export const Route=createFileRoute("/_authed/clusters")({component:ClustersPage});
