using System.Diagnostics;
using Prometheus;

namespace Commerce.Api.Observability;

public static class Telemetry
{
    public static readonly ActivitySource Source = new("Commerce.Api", "1.0.0");

    public static readonly Counter ShopOperations = Metrics.CreateCounter(
        "netdosuga_shop_operations_total",
        "Shop operations handled by commerce service.",
        "operation",
        "status");

    public static string OperationName(string operation) => operation switch
    {
        "catalog.list" or "applications.list" or "applications.create" or "applications.approve" or "applications.reject" or "offers.list" or "offers.update" or "offers.publish" or "cart.get" or "cart.add" or "cart.remove" or "orders.create" or "orders.list" or "orders.get" or "orders.pay" or "orders.cancel" or "tickets.redeem" or "sales.get" => operation,
        _ => "unknown"
    };
}
