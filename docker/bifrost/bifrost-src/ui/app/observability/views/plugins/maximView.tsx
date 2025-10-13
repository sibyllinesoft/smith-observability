import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { MaximConfigSchema, MaximFormSchema } from "@/lib/types/schemas";
import { useMemo } from "react";
import { toast } from "sonner";
import { MaximFormFragment } from "../../fragments/maximFormFragment";

export default function MaximView() {
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const [updatePlugin, { isLoading: isUpdatingPlugin }] = useUpdatePluginMutation();
	const currentConfig = useMemo(
		() => ({ ...((selectedPlugin?.config as MaximConfigSchema) ?? {}), enabled: selectedPlugin?.enabled }),
		[selectedPlugin],
	);

	const handleMaximConfigSave = (config: MaximFormSchema): Promise<void> => {
		return new Promise((resolve, reject) => {
			updatePlugin({
				name: "maxim",
				data: {
					enabled: config.enabled,
					config: config.maxim_config,
				},
			})
				.unwrap()
				.then(() => {
					toast.success("Maxim configuration updated successfully");
					resolve();
				})
				.catch((err) => {
					toast.error("Failed to update Maxim configuration", {
						description: getErrorMessage(err),
					});
					reject(err);
				});
		});
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="border-secondary flex w-full flex-col gap-2 rounded-sm border p-4">
				<div className="text-muted-foreground text-xs font-medium">Configuration</div>
				<div className="text-muted-foreground mb-2 text-xs font-normal">
					You can send in header <code>x-bf-log-repo-id</code> with a repository ID to log to a specific repository.
				</div>
				<MaximFormFragment onSave={handleMaximConfigSave} initialConfig={currentConfig} />
			</div>
		</div>
	);
}
