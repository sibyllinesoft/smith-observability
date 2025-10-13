"use client";

import ModelProviderConfig from "@/app/providers/views/modelProviderConfig";
import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderNames } from "@/lib/constants/logs";
import {
	getErrorMessage,
	setSelectedProvider,
	useAppDispatch,
	useAppSelector,
	useGetProvidersQuery,
	useLazyGetProviderQuery
} from "@/lib/store";
import { KnownProvider, ModelProviderName } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { PlusIcon, Trash } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import AddCustomProviderDialog from "./dialogs/addNewCustomProviderDialog";
import ConfirmDeleteProviderDialog from "./dialogs/confirmDeleteProviderDialog";
import ConfirmRedirectionDialog from "./dialogs/confirmRedirection";

export default function Providers() {
	const dispatch = useAppDispatch();
	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const providerFormIsDirty = useAppSelector((state) => state.provider.isDirty);

	const [showRedirectionDialog, setShowRedirectionDialog] = useState(false);
	const [showDeleteProviderDialog, setShowDeleteProviderDialog] = useState(false);
	const [pendingRedirection, setPendingRedirection] = useState<string | undefined>(undefined);
	const [showCustomProviderDialog, setShowCustomProviderDialog] = useState(false);
	const [provider, setProvider] = useQueryState("provider");

	const { data: savedProviders, isLoading: isLoadingProviders } = useGetProvidersQuery();
	const [getProvider, { isLoading: isLoadingProvider }] = useLazyGetProviderQuery();

	const allProviders = ProviderNames.map((p) => savedProviders?.find((provider) => provider.name === p) ?? { name: p, keys: [] }).sort(
		(a, b) => a.name.localeCompare(b.name),
	);
	const customProviders =
		savedProviders
			?.filter((provider) => !ProviderNames.includes(provider.name as KnownProvider))
			.sort((a, b) => a.name.localeCompare(b.name)) ?? [];

	useEffect(() => {
		if (!provider) return;
		const newSelectedProvider = allProviders.find((p) => p.name === provider) ?? customProviders.find((p) => p.name === provider);
		if (newSelectedProvider) {
			dispatch(setSelectedProvider(newSelectedProvider));
		}
		// We also try to fetch the latest version
		getProvider(provider)
			.unwrap()
			.then(() => {})
			.catch((err) => {
				if (err.status === 404) {
					// Initializing provider config with default values
					dispatch(
						setSelectedProvider({
							name: provider as ModelProviderName,
							keys: [],
							concurrency_and_buffer_size: DefaultPerformanceConfig,
							network_config: DefaultNetworkConfig,
							custom_provider_config: undefined,
							proxy_config: undefined,
							send_back_raw_response: undefined,
						}),
					);
					return;
				}
				toast.error("Something went wrong", {
					description: `We encountered an error while getting provider config: ${getErrorMessage(err)}`,
				});
			});
		return;
	}, [provider]);

	useEffect(() => {
		if (selectedProvider || !allProviders || allProviders.length === 0 || provider) return;
		setProvider(allProviders[0].name);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [selectedProvider, allProviders]);

	if (isLoadingProviders) {
		return <FullPageLoader />;
	}

	return (
		<div className="flex h-full flex-row gap-4">
			<ConfirmDeleteProviderDialog
				provider={selectedProvider!}
				show={showDeleteProviderDialog}
				onCancel={() => setShowDeleteProviderDialog(false)}
				onDelete={() => {
					setProvider(allProviders[0].name);
					setShowDeleteProviderDialog(false);
				}}
			/>
			<ConfirmRedirectionDialog
				show={showRedirectionDialog}
				onCancel={() => setShowRedirectionDialog(false)}
				onContinue={() => {
					setShowRedirectionDialog(false);
					if (pendingRedirection) setProvider(pendingRedirection);
					setPendingRedirection(undefined);
				}}
			/>
			<AddCustomProviderDialog
				show={showCustomProviderDialog}
				onSave={(id) => {
					setTimeout(() => {
						setProvider(id);
					}, 300);
					setShowCustomProviderDialog(false);
				}}
				onClose={() => {
					setShowCustomProviderDialog(false);
				}}
			/>
			<div className="flex flex-col">
				<TooltipProvider>
					<div className="flex w-[250px] flex-col gap-2 pb-10">
						<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
							{/* Standard Providers */}
							<div className="mb-4">
								<div className="text-muted-foreground mb-2 text-xs font-medium">Standard Providers</div>
								{allProviders.map((p) => {
									return (
										<Tooltip key={p.name}>
											<TooltipTrigger
												className={cn(
													"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
													selectedProvider?.name === p.name
														? "bg-secondary opacity-100 hover:opacity-100"
														: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
												)}
												onClick={(e) => {
													e.preventDefault();
													e.stopPropagation();
													if (providerFormIsDirty) {
														setPendingRedirection(p.name);
														setShowRedirectionDialog(true);
														return;
													}
													setProvider(p.name);
												}}
												asChild
											>
												<span>
													{p.custom_provider_config ? (
														<>
															<RenderProviderIcon
																provider={p.custom_provider_config?.base_provider_type as ProviderIconType}
																size="sm"
																className="h-4 w-4"
															/>
															<div className="text-sm">{p.name}</div>
														</>
													) : (
														<>
															<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
															<div className="text-sm">{ProviderLabels[p.name as keyof typeof ProviderLabels]}</div>
														</>
													)}
												</span>
											</TooltipTrigger>
										</Tooltip>
									);
								})}
								{customProviders.length > 0 && <div className="text-muted-foreground mb-2 text-xs font-medium">Custom Providers</div>}
								{customProviders.map((p) => (
									<Tooltip key={p.name}>
										<TooltipTrigger
											className={cn(
												"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
												selectedProvider?.name === p.name
													? "bg-secondary opacity-100 hover:opacity-100"
													: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
											)}
											onClick={(e) => {
												e.preventDefault();
												e.stopPropagation();
												if (providerFormIsDirty) {
													setPendingRedirection(p.name);
													setShowRedirectionDialog(true);
													return;
												}
												setProvider(p.name);
											}}
											asChild
										>
											<div className="group flex w-full items-center gap-2">
												<div className="flex w-full items-center gap-3">
													{p.custom_provider_config ? (
														<>
															<RenderProviderIcon
																provider={p.custom_provider_config?.base_provider_type as ProviderIconType}
																size="sm"
																className="h-4 w-4"
															/>
															<div className="text-sm">{p.name}</div>
														</>
													) : (
														<>
															<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
															<div className="text-sm">{ProviderLabels[p.name as keyof typeof ProviderLabels]}</div>
														</>
													)}
												</div>
												{selectedProvider?.name === p.name && (
													<Trash
														className="text-muted-foreground hover:text-destructive ml-auto hidden h-4 w-4 cursor-pointer group-hover:block"
														onClick={(event) => {
															event.preventDefault();
															event.stopPropagation();
															setShowDeleteProviderDialog(true);
														}}
													/>
												)}
											</div>
										</TooltipTrigger>
									</Tooltip>
								))}
							</div>
							<div className="my-4">
								<Button
									variant="outline"
									size="sm"
									className="w-full justify-start"
									onClick={(e) => {
										e.preventDefault();
										e.stopPropagation();
										setShowCustomProviderDialog(true);
									}}
								>
									<PlusIcon className="h-4 w-4" />
									<div className="text-xs">Add New Provider</div>
								</Button>
							</div>
						</div>
					</div>
				</TooltipProvider>
			</div>
			{isLoadingProvider && (
				<div className="bg-muted/10 flex w-full items-center justify-center rounded-md" style={{ maxHeight: "calc(100vh - 300px)" }}>
					<FullPageLoader />
				</div>
			)}
			{!selectedProvider && (
				<div className="bg-muted/10 flex w-full items-center justify-center rounded-md" style={{ maxHeight: "calc(100vh - 300px)" }}>
					<div className="text-muted-foreground text-sm">Select a provider</div>
				</div>
			)}
			{!isLoadingProvider && selectedProvider && <ModelProviderConfig provider={selectedProvider} />}
		</div>
	);
}
